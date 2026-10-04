package boards

import (
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
)

func TestTxHelpers(t *testing.T) {
	s, st := newService(t)
	ctx := t.Context()
	err := st.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := CreateTx(ctx, tx, Detail{ID: "b_a", Slug: "a", Name: "A", Position: 1}); err != nil {
			return err
		}
		if err := CreateTx(ctx, tx, Detail{ID: "b_b", Slug: "b", Name: "B", Position: 0}); err != nil {
			return err
		}
		if err := UpdateTx(ctx, tx, Detail{ID: "b_a", Slug: "a2", Name: "A two", Position: 2}); err != nil {
			return err
		}
		if err := UpdateTx(ctx, tx, Detail{ID: "b_missing", Slug: "m", Name: "M"}); !errors.Is(err, ErrNotFound) {
			t.Errorf("update of a missing board: %v", err)
		}
		if _, err := AppendLayoutTx(ctx, tx, "b_b", 1, json.RawMessage(`{}`), json.RawMessage(`{"x": {"href": "javascript:alert(1)"}}`), ""); !errors.Is(err, ErrInvalidTiles) {
			t.Errorf("invalid tiles: %v", err)
		}
		for i := range KeepRevisions + 2 {
			rev, err := AppendLayoutTx(ctx, tx, "b_a", 1, json.RawMessage(`{ "n": `+string(rune('0'+i%10))+` }`), json.RawMessage(`{"x": {"name": "X"}}`), "")
			if err != nil {
				return err
			}
			if rev != int64(i+1) {
				t.Errorf("revision %d, want %d", rev, i+1)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	all, err := ReadAll(ctx, st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].ID != "b_b" || all[0].Layout.Document != nil || string(all[0].Layout.Tiles) != EmptyTiles ||
		all[1].Slug != "a2" || all[1].Name != "A two" || all[1].Layout.Revision != KeepRevisions+2 ||
		string(all[1].Layout.Document) != `{"n":1}` || string(all[1].Layout.Tiles) != `{"x":{"name":"X"}}` {
		t.Fatalf("boards = %+v", all)
	}
	var n int
	if err := st.Reader().QueryRow(`SELECT count(*) FROM layouts WHERE board_id = 'b_a'`).Scan(&n); err != nil || n != KeepRevisions {
		t.Fatalf("history = %d, want %d", n, KeepRevisions)
	}
	// The optimistic save continues from the appended revision, and carries
	// its tiles over.
	if rev, err := s.SaveLayout(ctx, "b_a", KeepRevisions+2, 1, json.RawMessage(`{}`), nil, ""); err != nil || rev != KeepRevisions+3 {
		t.Fatalf("save after append: %d %v", rev, err)
	}
	if l, err := s.Layout(ctx, "b_a"); err != nil || string(l.Tiles) != `{"x":{"name":"X"}}` {
		t.Fatalf("tiles after save = %s %v", l.Tiles, err)
	}
}
