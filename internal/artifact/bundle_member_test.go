package artifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"strings"
	"testing"
)

func TestBundleMemberRejectsLinksTraversalAndOversize(t *testing.T) {
	archive := func(header tar.Header, body string) []byte {
		var b bytes.Buffer
		z := gzip.NewWriter(&b)
		tw := tar.NewWriter(z)
		if err := tw.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if body != "" {
			tw.Write([]byte(body))
		}
		tw.Close()
		z.Close()
		return b.Bytes()
	}
	valid := archive(tar.Header{Name: "deploy/docker-compose.yaml", Mode: 0600, Typeflag: tar.TypeReg, Size: 2}, "ok")
	var out strings.Builder
	if err := copyBundleMember(context.Background(), bytes.NewReader(valid), "deploy/docker-compose.yaml", &out, 1024); err != nil || out.String() != "ok" {
		t.Fatal("成员内容错误", err)
	}
	for _, input := range [][]byte{archive(tar.Header{Name: "deploy/docker-compose.yaml", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}, ""), archive(tar.Header{Name: "../outside", Typeflag: tar.TypeReg, Size: 2}, "ok"), valid} {
		out.Reset()
		if err := copyBundleMember(context.Background(), bytes.NewReader(input), "deploy/docker-compose.yaml", &out, 1); err == nil {
			t.Fatal("不安全归档不应读取成功")
		}
	}
}
