package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// 这组测试是 issue #5 的**结构性守卫**：它直接读 apps/worker/main.go 的语法树，
// 断言「推进数据集状态的那次调用」没有被放进逐题循环里。
//
// 为什么必须有它：batchPersist 的单测只能证明「重复调用 flushOnce 会报错」，
// 但如果有人把 flushOnce 挪回 for 循环内（每道题各自 flush 一次），
// 那些单测依然全绿 —— 而 issue #5 就复现了。所以这里直接检查调用点的嵌套位置。

const workerSourcePath = "main.go"

// statusAdvancingFuncs 是含「整批落库」调用的 handler。
//
// 判定规则：函数体内名为 Insert 的调用不得出现在任何 for / range 内部。
// 该名字是 ReasoningStore.Insert / RewardStore.Insert 的方法名，
// 也是唯一会推进 datasets.status 的入口。
var statusAdvancingFuncs = []string{
	"handleReasoningGeneration",
	"handleRewardGeneration",
}

func TestStatusAdvancingInsertIsNotInsidePerQuestionLoop(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, workerSourcePath, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", workerSourcePath, err)
	}

	for _, funcName := range statusAdvancingFuncs {
		fn := findFuncDecl(file, funcName)
		if fn == nil {
			t.Fatalf("未找到函数 %s：本守卫依赖该函数名，改名时必须同步更新", funcName)
		}

		violations := insertCallsInsideLoops(fset, fn)
		for _, pos := range violations {
			t.Errorf("%s: Insert 在逐题循环内被调用（%s）——"+
				"这会让第一条记录写完就写死 datasets.status，即 issue #5。",
				funcName, pos)
		}
	}
}

// 反向自检：确认守卫真的能识别「Insert 在循环内」这种形态。
//
// 没有这条测试，上面那条断言可能只是因为解析逻辑坏了而恒真（假绿）。
func TestLoopGuardDetectsInsertInsideLoop(t *testing.T) {
	const source = `package main

func handleReasoningGeneration() {
	for _, question := range questions {
		_ = question
		if err := reasoningStore.Insert(ctx, datasetID, records); err != nil {
			return
		}
	}
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthetic.go", source, parser.ParseComments)
	if err != nil {
		t.Fatalf("解析合成用例失败: %v", err)
	}

	fn := findFuncDecl(file, "handleReasoningGeneration")
	if fn == nil {
		t.Fatal("未找到合成用例函数")
	}
	if found := insertCallsInsideLoops(fset, fn); len(found) != 1 {
		t.Fatalf("守卫必须识别出循环内的 Insert，实际找到 %d 处", len(found))
	}

	// 对照：同样的调用放在循环外，守卫必须放行。
	const okSource = `package main

func handleReasoningGeneration() {
	for _, question := range questions {
		_ = question
	}
	if err := reasoningStore.Insert(ctx, datasetID, records); err != nil {
		return
	}
}
`
	okFile, err := parser.ParseFile(fset, "synthetic_ok.go", okSource, parser.ParseComments)
	if err != nil {
		t.Fatalf("解析对照用例失败: %v", err)
	}
	okFn := findFuncDecl(okFile, "handleReasoningGeneration")
	if found := insertCallsInsideLoops(fset, okFn); len(found) != 0 {
		t.Fatalf("循环外的 Insert 不应被判违规，实际找到 %d 处", len(found))
	}
}

// insertCallsInsideLoops 返回函数体内「嵌套在 for/range 里」的 Insert 调用位置。
//
// 为什么不用 ast.Inspect：它只提供进入节点的回调，没有「离开节点」信号，
// 无法维护循环深度。这里显式递归并携带深度。
func insertCallsInsideLoops(fset *token.FileSet, fn *ast.FuncDecl) []token.Position {
	var found []token.Position

	var walk func(node ast.Node, loopDepth int)
	walk = func(node ast.Node, loopDepth int) {
		if node == nil {
			return
		}

		// 进入 for/range 时加深一层，并只递归其内部。
		switch typed := node.(type) {
		case *ast.ForStmt:
			walk(typed.Body, loopDepth+1)
			return
		case *ast.RangeStmt:
			walk(typed.Body, loopDepth+1)
			return
		case *ast.CallExpr:
			if selector, ok := typed.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "Insert" && loopDepth > 0 {
				found = append(found, fset.Position(typed.Pos()))
			}
		}

		// 其余节点按子节点继续递归，保持当前深度。
		for _, child := range childNodes(node) {
			walk(child, loopDepth)
		}
	}

	walk(fn.Body, 0)
	return found
}

// stmtNodes / exprNodes 把具体切片转成 []ast.Node。
//
// Go 的切片类型不协变，*ast.BlockStmt.List 是 []ast.Stmt、CallExpr.Args 是 []ast.Expr，
// 都需要显式转换后才能统一递归。
func stmtNodes(stmts []ast.Stmt) []ast.Node {
	nodes := make([]ast.Node, 0, len(stmts))
	for _, stmt := range stmts {
		nodes = append(nodes, stmt)
	}
	return nodes
}

func exprNodes(exprs []ast.Expr) []ast.Node {
	nodes := make([]ast.Node, 0, len(exprs))
	for _, expr := range exprs {
		nodes = append(nodes, expr)
	}
	return nodes
}

// childNodes 返回一个节点的直接子节点。
//
// 只列出本守卫需要穿透的节点类型：函数体、块、if、表达式语句、赋值、
// 声明、返回、以及 range/for 的头部表达式。保持显式列表比反射更可读，
// 也避免漏掉节点时静默放过违规。
func childNodes(node ast.Node) []ast.Node {
	switch typed := node.(type) {
	case *ast.BlockStmt:
		return stmtNodes(typed.List)
	case *ast.IfStmt:
		nodes := []ast.Node{typed.Init, typed.Cond, typed.Body}
		if typed.Else != nil {
			nodes = append(nodes, typed.Else)
		}
		return nodes
	case *ast.ExprStmt:
		return []ast.Node{typed.X}
	case *ast.AssignStmt:
		nodes := exprNodes(typed.Lhs)
		return append(nodes, exprNodes(typed.Rhs)...)
	case *ast.DeclStmt:
		return []ast.Node{typed.Decl}
	case *ast.GenDecl:
		nodes := make([]ast.Node, 0, len(typed.Specs))
		for _, spec := range typed.Specs {
			nodes = append(nodes, spec)
		}
		return nodes
	case *ast.ValueSpec:
		return exprNodes(typed.Values)
	case *ast.ReturnStmt:
		return exprNodes(typed.Results)
	case *ast.GoStmt:
		return []ast.Node{typed.Call}
	case *ast.DeferStmt:
		return []ast.Node{typed.Call}
	case *ast.CallExpr:
		return exprNodes(typed.Args)
	case *ast.ParenExpr:
		return []ast.Node{typed.X}
	case *ast.UnaryExpr:
		return []ast.Node{typed.X}
	case *ast.BinaryExpr:
		return []ast.Node{typed.X, typed.Y}
	case *ast.LabeledStmt:
		return []ast.Node{typed.Stmt}
	case *ast.SwitchStmt:
		nodes := []ast.Node{typed.Init, typed.Tag, typed.Body}
		return nodes
	case *ast.TypeSwitchStmt:
		return []ast.Node{typed.Init, typed.Assign, typed.Body}
	case *ast.CaseClause:
		return stmtNodes(typed.Body)
	case *ast.CommClause:
		return stmtNodes(typed.Body)
	case *ast.SelectStmt:
		return []ast.Node{typed.Body}
	case *ast.FuncLit:
		return []ast.Node{typed.Body}
	default:
		return nil
	}
}

func findFuncDecl(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}
