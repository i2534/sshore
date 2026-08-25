package sftp

import "testing"

func TestParseLsLf(t *testing.T) {
	out := "total 12\n" +
		"drwxr-xr-x    2 user group     4096 Aug 25 10:00 logs\n" +
		"-rw-r--r--    1 user group      123 Aug 25 10:01 app.log\n" +
		"-rw-r--r--    1 user group       45 Aug 25 10:02 my file.txt\n"
	items, err := ParseLsLf(out)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("want 3 items got %d: %+v", len(items), items)
	}
	if !items[0].IsDir || items[0].Name != "logs" {
		t.Fatalf("logs should be dir: %+v", items[0])
	}
	if items[1].IsDir || items[1].Name != "app.log" || items[1].Size != 123 {
		t.Fatalf("app.log wrong: %+v", items[1])
	}
	if items[2].Name != "my file.txt" {
		t.Fatalf("space filename should parse: %+v", items[2])
	}
}

func TestParseLsLfRealSftpOutput(t *testing.T) {
	out := "sftp> ls -l /tmp/x/remotedir\n" +
		"-rw-rw-r--    1 lan      lan             6 Aug 25 15:34 a.txt\n" +
		"-rw-rw-r--    1 lan      lan            10 Aug 25 15:34 b.log\n"
	items, err := ParseLsLf(out)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items got %d: %+v", len(items), items)
	}
	if items[0].Name != "a.txt" || items[0].Size != 6 {
		t.Fatalf("a.txt wrong: %+v", items[0])
	}
}

func TestParseLsLfEmptyIsEmptyList(t *testing.T) {
	items, err := ParseLsLf("sftp> ls -l .\n")
	if err != nil {
		t.Fatalf("empty listing should not error: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("want 0 items got %d", len(items))
	}
}

func TestParseLsLfModTime(t *testing.T) {
	out := "-rw-r--r--    1 lan      lan             6 Aug 25 15:34 a.txt\n" +
		"-rw-r--r--    1 lan      lan            10 Aug 25 2024 old.log\n"
	items, err := ParseLsLf(out)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// Current-year file gets HH:MM; must NOT include the filename.
	if items[0].ModTime != "2026-08-25 15:34" {
		t.Fatalf("this-year modTime wrong (should be YYYY-MM-DD HH:MM, no filename): %q", items[0].ModTime)
	}
	// Old file shows a bare year.
	if items[1].ModTime != "2024-08-25" {
		t.Fatalf("old-file modTime wrong: %q", items[1].ModTime)
	}
}
