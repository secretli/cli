package cli

import (
	"reflect"
	"strings"
	"testing"
)

func TestPickFiles(t *testing.T) {
	names := []string{"holiday.mov", "notes.txt", "IMG_0001.JPG", "IMG_0002.jpg", "file[1].txt", "2"}
	tests := []struct {
		answer string
		want   []int
	}{
		{"", []int{0, 1, 2, 3, 4, 5}},
		{"   ", []int{0, 1, 2, 3, 4, 5}},
		{"2", []int{1}},
		{"1 3", []int{0, 2}},
		{"3,1", []int{0, 2}},
		{"1, 3", []int{0, 2}},
		{"2-4", []int{1, 2, 3}},
		{"4-2", []int{1, 2, 3}},
		{"1 1 1-2", []int{0, 1}},
		{"*.jpg", []int{2, 3}},
		{"img_*", []int{2, 3}},
		{"notes.txt", []int{1}},
		{"NOTES.TXT", []int{1}},
		{"*.txt 1", []int{0, 1, 4}},
		{"file[1].txt", []int{4}},
		{"6", []int{5}},
		{"*", []int{0, 1, 2, 3, 4, 5}},
	}
	for _, tt := range tests {
		got, err := pickFiles(tt.answer, names)
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("pickFiles(%q) = %v, %v; want %v", tt.answer, got, err, tt.want)
		}
	}
}

func TestPickFilesRefusesWhatIsNotThere(t *testing.T) {
	names := []string{"a.txt", "b.txt", "c.txt"}
	tests := []struct {
		answer, want string
	}{
		{"0", "there is no file 0; the files are numbered 1 to 3"},
		{"4", "there is no file 4; the files are numbered 1 to 3"},
		{"2-9", "there is no file 2-9; the files are numbered 1 to 3"},
		{"*.png", `no file is called "*.png"`},
		{"1 d.txt", `no file is called "d.txt"`},
		{"1-", `no file is called "1-"`},
		{"[", `no file is called "["`},
	}
	for _, tt := range tests {
		got, err := pickFiles(tt.answer, names)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("pickFiles(%q) = %v, %v; want the error %q", tt.answer, got, err, tt.want)
		}
	}
}
