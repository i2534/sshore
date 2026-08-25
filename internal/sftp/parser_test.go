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
