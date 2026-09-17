package source

import (
	"errors"
	"testing"
)

// TestNormalizeListLimit 归一规则：<=0 默认、>500 截断、边界值保留。
func TestNormalizeListLimit(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{-1, DefaultListLimit},
		{0, DefaultListLimit},
		{1, 1},
		{100, 100},
		{500, MaxListLimit},
		{501, MaxListLimit},
		{10000, MaxListLimit},
	}
	for _, tc := range cases {
		if got := NormalizeListLimit(tc.in); got != tc.want {
			t.Errorf("NormalizeListLimit(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestListOffsetCursorRoundTrip：编码-解码往返稳定；非法 token 拒绝。
func TestListOffsetCursorRoundTrip(t *testing.T) {
	for _, offset := range []int{0, 1, 99, 500, 1 << 20} {
		cursor := EncodeListOffset(offset)
		got, err := DecodeListOffset(cursor)
		if err != nil {
			t.Fatalf("DecodeListOffset(%q) = %v, want %d", cursor, err, offset)
		}
		if got != offset {
			t.Errorf("DecodeListOffset(EncodeListOffset(%d)) = %d", offset, got)
		}
	}

	// 空串是「从头开始」的合法输入。
	if got, err := DecodeListOffset(""); err != nil || got != 0 {
		t.Errorf("DecodeListOffset(\"\") = %d, %v; want 0, nil", got, err)
	}

	for _, cursor := range []string{"!!not-base64!!", EncodeListOffset(-3), "MALFORMED"} {
		if _, err := DecodeListOffset(cursor); !errors.Is(err, ErrInvalid) {
			t.Errorf("DecodeListOffset(%q) = %v, want ErrInvalid", cursor, err)
		}
	}
}

// TestPageSlice：切片分页语义——页边界、末页 EOF、越界 offset 按
// EOF 处理、非法 cursor 失败、limit 归一。
func TestPageSlice(t *testing.T) {
	entries := make([]FileInfo, 0, 7)
	for i := 0; i < 7; i++ {
		entries = append(entries, FileInfo{Path: string(rune('a' + i))})
	}

	// 第一页：limit 3。
	page, err := PageSlice(entries, ListOptions{Limit: 3})
	if err != nil {
		t.Fatalf("PageSlice page 1: %v", err)
	}
	if len(page.Entries) != 3 || page.Entries[0].Path != "a" || page.Entries[2].Path != "c" {
		t.Fatalf("page 1 = %+v, want a,b,c", page.Entries)
	}
	if page.NextCursor == "" {
		t.Fatal("page 1 NextCursor empty, want continuation")
	}

	// 第二页：带 cursor。
	cursor := page.NextCursor
	page2, err := PageSlice(entries, ListOptions{Limit: 3, Cursor: cursor})
	if err != nil {
		t.Fatalf("PageSlice page 2: %v", err)
	}
	if len(page2.Entries) != 3 || page2.Entries[0].Path != "d" {
		t.Fatalf("page 2 = %+v, want d,e,f", page2.Entries)
	}

	// 末页：不足 limit，EOF 无 cursor。
	page3, err := PageSlice(entries, ListOptions{Limit: 3, Cursor: page2.NextCursor})
	if err != nil {
		t.Fatalf("PageSlice page 3: %v", err)
	}
	if len(page3.Entries) != 1 || page3.Entries[0].Path != "g" {
		t.Fatalf("page 3 = %+v, want g", page3.Entries)
	}
	if page3.NextCursor != "" {
		t.Errorf("page 3 NextCursor = %q, want empty at EOF", page3.NextCursor)
	}

	// offset 正好等于长度：空页 EOF。
	end := EncodeListOffset(len(entries))
	pageEnd, err := PageSlice(entries, ListOptions{Limit: 3, Cursor: end})
	if err != nil {
		t.Fatalf("PageSlice at end: %v", err)
	}
	if len(pageEnd.Entries) != 0 || pageEnd.NextCursor != "" {
		t.Errorf("page at end = %+v, want empty EOF page", pageEnd)
	}

	// offset 超过长度：同样按 EOF 处理。
	over := EncodeListOffset(len(entries) + 10)
	pageOver, err := PageSlice(entries, ListOptions{Cursor: over})
	if err != nil {
		t.Fatalf("PageSlice past end: %v", err)
	}
	if len(pageOver.Entries) != 0 || pageOver.NextCursor != "" {
		t.Errorf("page past end = %+v, want empty EOF page", pageOver)
	}

	// 非法 cursor：ErrInvalid。
	if _, err := PageSlice(entries, ListOptions{Cursor: "bogus"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("PageSlice bogus cursor = %v, want ErrInvalid", err)
	}

	// limit 超上限：截断为 MaxListLimit（7 条一次取完）。
	pageAll, err := PageSlice(entries, ListOptions{Limit: 100000})
	if err != nil {
		t.Fatalf("PageSlice big limit: %v", err)
	}
	if len(pageAll.Entries) != 7 || pageAll.NextCursor != "" {
		t.Errorf("big-limit page = %d entries, cursor %q; want all 7, EOF", len(pageAll.Entries), pageAll.NextCursor)
	}

	// 默认 limit：Limit 0 取 DefaultListLimit。
	pageDefault, err := PageSlice(entries, ListOptions{})
	if err != nil {
		t.Fatalf("PageSlice default limit: %v", err)
	}
	if len(pageDefault.Entries) != 7 {
		t.Errorf("default page = %d entries, want 7 (default >= len)", len(pageDefault.Entries))
	}
}
