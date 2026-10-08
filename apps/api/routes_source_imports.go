package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	importer "github.com/1420970597/llm/internal/import"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/jackc/pgx/v5"
)

func init() { RegisterRoutes(registerSourceImportRoutes) }
func registerSourceImportRoutes(mux *http.ServeMux, app *application) {
	base := projectPrefix + "/{projectId}"
	mux.HandleFunc("POST "+base+"/source-imports", app.uploadSourceDocument)
	mux.HandleFunc("GET "+base+"/source-imports", app.listSourceImports)
	mux.HandleFunc("GET "+base+"/source-imports/{importId}", app.getSourceImport)
	mux.HandleFunc("POST "+base+"/source-import-products", app.importSourceProducts)
	mux.HandleFunc("POST "+base+"/source-import-products/preview", app.previewSourceProducts)
	mux.HandleFunc("GET "+base+"/source-chunks", app.listSourceChunks)
	mux.HandleFunc("GET "+base+"/source-chunks/{chunkId}", app.getSourceChunk)
}

func sourceFileKind(fileName string) (string, bool) {
	switch strings.ToLower(filepath.Ext(fileName)) {
	case ".md", ".markdown":
		return "markdown", true
	case ".txt":
		return "txt", true
	default:
		return "", false
	}
}

func (app *application) uploadSourceDocument(w http.ResponseWriter, r *http.Request) {
	project, _, ok := app.requireProject(w, r, store.AuthzRun)
	if !ok {
		return
	}
	user, _ := requestUser(r)
	if r.ContentLength > importer.MaxSourceUploadBytes+(1<<20) {
		app.writeAPIError(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "素材文件不能超过 200 MB", nil)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, importer.MaxSourceUploadBytes+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			app.writeAPIError(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "素材文件不能超过 200 MB", nil)
		} else {
			app.writeAPIError(w, r, http.StatusBadRequest, codeValidation, "上传格式有误，请选择 Markdown/TXT 文件", nil)
		}
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, header, err := r.FormFile("file")
	if err != nil {
		app.writeAPIError(w, r, http.StatusBadRequest, codeValidation, "请选择素材文件", nil)
		return
	}
	defer file.Close()
	kind, supported := sourceFileKind(header.Filename)
	if !supported {
		app.writeAPIError(w, r, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "目前仅支持 Markdown/TXT；PDF/DOCX 尚未支持，请转换后上传", nil)
		return
	}
	if header.Size > importer.MaxSourceUploadBytes {
		app.writeAPIError(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "素材文件不能超过 200 MB", nil)
		return
	}
	body, err := io.ReadAll(io.LimitReader(file, importer.MaxSourceUploadBytes+1))
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(body) > importer.MaxSourceUploadBytes {
		app.writeAPIError(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "素材文件不能超过 200 MB", nil)
		return
	}
	options, fieldErrors := sourceOptionsFromForm(r)
	if len(fieldErrors) > 0 {
		app.writeAPIError(w, r, http.StatusUnprocessableEntity, codeValidation, "请检查素材参数", fieldErrors)
		return
	}
	options.ActorID = user.ID
	options.FileName = filepath.Base(header.Filename)
	options.Kind = kind
	warnings := []string{}
	mime := strings.Split(header.Header.Get("Content-Type"), ";")[0]
	if mime != "text/plain" && mime != "text/markdown" {
		warnings = append(warnings, "文件媒体类型与扩展名不一致，已按扩展名解析；需要有效的 UTF-8 文本")
	}
	row, replay, err := store.NewLegacyImportStore(app.store.DB()).QueueSourceImport(r.Context(), store.QueueSourceImportInput{ProjectID: project.ID, SourceKind: store.SourceKindDocument, SourceKey: r.FormValue("sourceKey"), Content: body, Options: options})
	if err != nil {
		app.writeSourceImportError(w, r, err)
		return
	}
	app.writeSourceImportResult(w, row, replay, warnings)
}

func sourceOptionsFromForm(r *http.Request) (store.SourceImportOptions, []model.FieldError) {
	options := store.SourceImportOptions{ChangeReason: r.FormValue("changeReason"), Chunking: model.DefaultSourceChunking()}
	errs := []model.FieldError{}
	if strings.TrimSpace(options.ChangeReason) == "" {
		errs = append(errs, model.FieldError{Field: "changeReason", Message: "变更理由必填"})
	}
	if raw := r.FormValue("expectedRevision"); raw != "" {
		revision, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || revision < 0 {
			errs = append(errs, model.FieldError{Field: "expectedRevision", Message: "必须为非负整数"})
		} else {
			options.ExpectedRevision = revision
		}
	} else {
		errs = append(errs, model.FieldError{Field: "expectedRevision", Message: "请刷新素材版本后重新上传"})
	}
	if value := r.FormValue("chunking.algorithm"); value != "" {
		options.Chunking.Algorithm = value
	}
	if _, present := r.MultipartForm.Value["chunking.separator"]; present {
		options.Chunking.Separator = r.FormValue("chunking.separator")
	}
	for _, field := range []struct {
		name   string
		target *int
	}{{"chunking.maxLength", &options.Chunking.MaxLength}, {"chunking.minLength", &options.Chunking.MinLength}} {
		if raw := r.FormValue(field.name); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				errs = append(errs, model.FieldError{Field: field.name, Message: "必须为整数"})
			} else {
				*field.target = n
			}
		}
	}
	if raw := r.FormValue("chunking.keepHeadingPath"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			errs = append(errs, model.FieldError{Field: "chunking.keepHeadingPath", Message: "必须为 true 或 false"})
		} else {
			options.Chunking.KeepHeadingPath = value
		}
	}
	if err := model.ValidateSourcePayload(model.SourcePayload{SchemaVersion: "source.v1", Chunking: options.Chunking}); err != nil {
		fields, _ := model.HasFieldErrors(err)
		errs = append(errs, fields...)
	}
	return options, errs
}

type sourceProductRequest struct {
	Format        string `json:"format"`
	SourceKey     string `json:"sourceKey"`
	Content       string `json:"content"`
	ContentBase64 string `json:"contentBase64"`
	TargetKind    string `json:"targetKind"`
	ChangeReason  string `json:"changeReason"`
}

func (app *application) decodeSourceProductRequest(w http.ResponseWriter, r *http.Request) (sourceProductRequest, []byte, bool) {
	var input sourceProductRequest
	r.Body = http.MaxBytesReader(w, r.Body, 28<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			app.writeAPIError(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "产物文件不能超过 20 MB", nil)
		} else {
			app.writeAPIError(w, r, http.StatusBadRequest, codeValidation, "产物请求格式不正确", nil)
		}
		return input, nil, false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		app.writeAPIError(w, r, http.StatusBadRequest, codeValidation, "产物请求只能包含一个 JSON 对象", nil)
		return input, nil, false
	}
	if input.Content != "" && input.ContentBase64 != "" {
		app.writeAPIError(w, r, http.StatusBadRequest, codeValidation, "content 和 contentBase64 只能选择一种", nil)
		return input, nil, false
	}
	content := []byte(input.Content)
	if input.ContentBase64 != "" {
		var err error
		content, err = base64.StdEncoding.DecodeString(input.ContentBase64)
		if err != nil {
			app.writeAPIError(w, r, http.StatusBadRequest, codeValidation, "contentBase64 编码不正确", nil)
			return input, nil, false
		}
	}
	if len(content) > importer.MaxProductUploadBytes {
		app.writeAPIError(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "产物文件不能超过 20 MB", nil)
		return input, nil, false
	}
	if strings.TrimSpace(input.SourceKey) == "" {
		app.writeAPIError(w, r, http.StatusUnprocessableEntity, codeValidation, "来源键必填", []model.FieldError{{Field: "sourceKey", Message: "例如 easy-dataset/project-v1"}})
		return input, nil, false
	}
	return input, content, true
}

func (app *application) importSourceProducts(w http.ResponseWriter, r *http.Request) {
	project, _, ok := app.requireProject(w, r, store.AuthzRun)
	if !ok {
		return
	}
	user, _ := requestUser(r)
	input, content, ok := app.decodeSourceProductRequest(w, r)
	if !ok {
		return
	}
	row, replay, err := store.NewLegacyImportStore(app.store.DB()).QueueSourceImport(r.Context(), store.QueueSourceImportInput{ProjectID: project.ID, SourceKind: store.SourceKindProduct, SourceKey: input.SourceKey, Content: content, Options: store.SourceImportOptions{ActorID: user.ID, Format: input.Format, TargetKind: input.TargetKind, ChangeReason: input.ChangeReason}})
	if err != nil {
		app.writeSourceImportError(w, r, err)
		return
	}
	app.writeSourceImportResult(w, row, replay, []string{"成品导入不生成问题、不消耗模型预算、不参与素材接地"})
}
func (app *application) previewSourceProducts(w http.ResponseWriter, r *http.Request) {
	_, _, ok := app.requireProject(w, r, store.AuthzRun)
	if !ok {
		return
	}
	input, content, ok := app.decodeSourceProductRequest(w, r)
	if !ok {
		return
	}
	_, preview, err := importer.MapProductRows(input.Format, content)
	if err != nil {
		app.writeAPIError(w, r, http.StatusUnprocessableEntity, codeValidation, err.Error(), nil)
		return
	}
	app.writeJSON(w, http.StatusOK, preview)
}
func (app *application) writeSourceImportResult(w http.ResponseWriter, row store.LegacyImport, replay bool, warnings []string) {
	status := http.StatusAccepted
	if replay {
		status = http.StatusOK
	}
	app.writeJSON(w, status, map[string]any{"importId": row.ID, "sourceKind": row.SourceKind, "sourceKey": row.SourceKey, "status": row.Status, "replay": replay, "counts": row.Counts, "jobId": row.JobID, "warnings": warnings})
}
func (app *application) writeSourceImportError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrSourceKeyConflict) {
		app.writeAPIError(w, r, http.StatusConflict, "SOURCE_KEY_CONFLICT", err.Error(), nil)
		return
	}
	if errors.Is(err, store.ErrLegacyImportNotFound) || errors.Is(err, pgx.ErrNoRows) {
		app.writeAPIError(w, r, http.StatusNotFound, codeNotFound, "未找到该素材或导入记录", nil)
		return
	}
	app.writeDocumentError(w, r, err)
}
func sourcePagination(r *http.Request) (int, int) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}
func (app *application) listSourceImports(w http.ResponseWriter, r *http.Request) {
	project, _, ok := app.requireProject(w, r, store.AuthzRead)
	if !ok {
		return
	}
	limit, offset := sourcePagination(r)
	items, total, err := store.NewLegacyImportStore(app.store.DB()).ListSourceImports(r.Context(), project.ID, limit, offset)
	if err != nil {
		app.writeSourceImportError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}
func (app *application) getSourceImport(w http.ResponseWriter, r *http.Request) {
	project, _, ok := app.requireProject(w, r, store.AuthzRead)
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("importId"), 10, 64)
	item, err := store.NewLegacyImportStore(app.store.DB()).GetSourceImport(r.Context(), project.ID, id)
	if err != nil {
		app.writeSourceImportError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusOK, item)
}
func (app *application) listSourceChunks(w http.ResponseWriter, r *http.Request) {
	project, _, ok := app.requireProject(w, r, store.AuthzRead)
	if !ok {
		return
	}
	limit, offset := sourcePagination(r)
	chunks := store.NewSourceChunkStore(app.store.DB())
	var items []model.SourceChunk
	var total int
	var err error
	if raw := r.URL.Query().Get("sourceVersionId"); raw != "" {
		versionID, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || versionID <= 0 {
			app.writeAPIError(w, r, http.StatusUnprocessableEntity, codeValidation, "来源版本 ID 不正确", []model.FieldError{{Field: "sourceVersionId", Message: "必须为正整数"}})
			return
		}
		items, total, err = chunks.ListSourceChunksAtVersion(r.Context(), project.ID, versionID, r.URL.Query().Get("sourceDocumentStableId"), r.URL.Query().Get("q"), limit, offset)
	} else {
		items, total, err = chunks.ListSourceChunks(r.Context(), project.ID, r.URL.Query().Get("sourceDocumentStableId"), r.URL.Query().Get("q"), limit, offset)
	}
	if err != nil {
		app.writeSourceImportError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}
func (app *application) getSourceChunk(w http.ResponseWriter, r *http.Request) {
	project, _, ok := app.requireProject(w, r, store.AuthzRead)
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("chunkId"), 10, 64)
	item, err := store.NewSourceChunkStore(app.store.DB()).GetSourceChunk(r.Context(), project.ID, id)
	if err != nil {
		app.writeSourceImportError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusOK, item)
}
