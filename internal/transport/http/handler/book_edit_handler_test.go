package handler

import (
	"strings"
	"testing"
)

func TestDecodeBookPatchPreservesOmittedAndClearValues(t *testing.T) {
	p, err := decodeBookPatch(strings.NewReader(`{"subtitle":"","authors":[],"published_year":2009}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != nil || p.Subtitle == nil || *p.Subtitle != "" || p.Authors == nil || len(*p.Authors) != 0 || p.PublishedYear == nil || *p.PublishedYear != 2009 {
		t.Fatalf("incorrect partial update: %+v", p)
	}
}

func TestDecodeBookPatchRejectsMalformedShapes(t *testing.T) {
	for _, body := range []string{`[]`, `true`, `{"title":`, `{"authors":{}}`, `{"subjects":[1]}`, `{"published_year":2009.5}`, `{"title":"a"} garbage`, `{"title":"a",}`, `{"ID":"x"}`, `{"copy_ids":[]}`, `{"accession_number":"A1"}`, `{"loan_policy":"circulating"}`, `{"lcc_class":"Q"}`, `{"wing":"North"}`} {
		t.Run(body, func(t *testing.T) {
			if _, err := decodeBookPatch(strings.NewReader(body)); err == nil {
				t.Fatal("invalid body accepted")
			}
		})
	}
}
