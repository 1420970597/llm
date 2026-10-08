package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductPublicFormatsAndFailureLineNumbers(t *testing.T) {
	content := []byte("{\"instruction\":\"问题\",\"input\":\"背景\",\"output\":\"答案\"}\n{\"instruction\":\"缺答案\"}\n{\"instruction\":[],\"output\":\"答案\"}\n{\"instruction\":\"问题\",\"input\":\"背景\",\"output\":\"答案\"}")
	rows, p, err := MapProductRows("alpaca", content)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || p.SourceItems != 4 || p.FailedItems != 2 || p.DuplicateItems != 1 || p.Failures[0].SourceID != 2 || p.Failures[1].SourceID != 3 {
		t.Fatalf("wrong preview %+v", p)
	}
	if rows[0].Payload["question"] != "问题\n\n背景" || rows[0].ContentHash != rows[1].ContentHash {
		t.Fatalf("mapping changed %+v", rows)
	}
}

func TestProductMultiTurnPreservedAndIncompleteRejected(t *testing.T) {
	rows, p, err := MapProductRows("sharegpt", []byte(`[{"conversations":[{"from":"system","value":"要求"},{"from":"human","value":"问题一"},{"from":"gpt","value":"答案一"},{"from":"human","value":"问题二"},{"from":"gpt","value":"答案二"}]}]`))
	if err != nil || len(rows) != 1 || p.FailedItems != 0 {
		t.Fatalf("map failed %v %+v", err, p)
	}
	if rows[0].Payload["question"] != "问题二" || rows[0].Payload["conversations"] == nil {
		t.Fatal("history lost")
	}
	_, p, err = MapProductRows("sharegpt", []byte(`[{"conversations":[{"from":"human","value":"问题"},{"from":"human","value":"不完整"}]}]`))
	if err != nil || p.FailedItems != 1 {
		t.Fatal("bad order accepted")
	}
	if _, _, err := MapProductRows("private_format", nil); err == nil {
		t.Fatal("unsupported private format accepted")
	}
	if _, _, err := MapProductRows("jsonl", []byte{0xff}); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	_, p, err = MapProductRows("alpaca", []byte(`{"instruction":"bad\u0000text","output":"a"}`))
	if err != nil || p.FailedItems != 1 {
		t.Fatal("NUL accepted as PostgreSQL text")
	}
	if _, _, err := MapProductRows("jsonl", []byte(strings.Repeat("{}\n", MaxProductRows+1))); err == nil {
		t.Fatal("oversize row count accepted")
	}
}

func TestProductContractFixtures(t *testing.T) {
	for _, format := range []string{"alpaca", "sharegpt", "jsonl"} {
		t.Run(format, func(t *testing.T) {
			content, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", "source-import", format+".jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			rows, p, err := MapProductRows(format, content)
			if err != nil || len(rows) != 1 || p.FailedItems != 0 || rows[0].Payload["source"] != "external_import" {
				t.Fatalf("contract failed %+v %v", p, err)
			}
		})
	}
}
