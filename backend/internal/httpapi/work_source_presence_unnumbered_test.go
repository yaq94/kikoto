package httpapi

import (
	"context"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
)

// Two unnumbered works share the empty code. Reading one work's sources by code
// used to return both works' sources, so a work detail could show another
// work's Kikoeru directory as its own.
func TestSourcePresenceByWorkKeepsUnnumberedWorksSeparate(t *testing.T) {
	db := openMigratedTestDB(t)
	if _, err := db.Exec(`
		INSERT INTO work (id, primary_code, title) VALUES
			(321, '', 'Example Work A'),
			(322, '', 'Example Work B');
		INSERT INTO file_source (id, code, display_name, source_type, enabled) VALUES
			(321, 'example_remote_a', 'Example Remote A', 'kikoeru_compatible', 1),
			(322, 'example_remote_b', 'Example Remote B', 'kikoeru_compatible', 1);
		INSERT INTO work_source_presence (work_id, file_source_id, presence_type, remote_id, remote_code, availability)
		VALUES
			(321, 321, 'source', '9001', 'RJ00000051', 'available'),
			(322, 322, 'source', '9002', 'RJ00000052', 'available');
	`); err != nil {
		t.Fatal(err)
	}
	server := NewServer(db, config.Config{})

	a := server.sourcePresenceForWork(context.Background(), 321, "")
	if len(a) != 1 || a[0].FileSourceID != 321 {
		t.Fatalf("work 321 presence = %#v, want only its own source", a)
	}
	b := server.sourcePresenceForWork(context.Background(), 322, "")
	if len(b) != 1 || b[0].FileSourceID != 322 {
		t.Fatalf("work 322 presence = %#v, want only its own source", b)
	}
	if shared := server.sourcePresenceForCode(context.Background(), ""); len(shared) != 0 {
		t.Fatalf("empty-code lookup returned %d presences, want none", len(shared))
	}
}

// A work without a release code still resolves its Kikoeru directory: the
// presence carries the code that source knows, and availability reads it.
func TestUnnumberedWorkResolvesPresenceCode(t *testing.T) {
	db := openMigratedTestDB(t)
	if _, err := db.Exec(`
		INSERT INTO work (id, primary_code, title) VALUES (331, '', 'Example Work');
		INSERT INTO file_source (id, code, display_name, source_type, enabled) VALUES
			(331, 'example_remote', 'Example Remote', 'kikoeru_compatible', 1);
		INSERT INTO work_source_presence (work_id, file_source_id, presence_type, remote_id, remote_code, availability)
		VALUES (331, 331, 'source', '9101', 'RJ00000053', 'available');
	`); err != nil {
		t.Fatal(err)
	}
	server := NewServer(db, config.Config{})
	items := server.sourcePresenceForWork(context.Background(), 331, "")
	if len(items) != 1 || items[0].RemoteCode != "RJ00000053" {
		t.Fatalf("presence = %#v, want the source's own code", items)
	}
	flags, err := server.sourceAvailabilityFlags(context.Background(), 331, items[0].RemoteCode)
	if err != nil {
		t.Fatal(err)
	}
	if flags.WorkID != nil {
		t.Fatalf("unnumbered work resolved by code: %#v", flags)
	}
	// The availability response still finds the presence by the code that
	// source knows, so an unnumbered work keeps its Kikoeru directory.
	response, err := server.readWorkSourceAvailability(context.Background(), items[0].RemoteCode)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, source := range response.Sources {
		if source.SourceID == 331 && source.HasRemote {
			found = true
		}
	}
	if !found {
		t.Fatalf("availability = %#v, want the presence matched by its source code", response.Sources)
	}
}
