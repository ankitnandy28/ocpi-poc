package main

import "testing"

func TestParseNextLink(t *testing.T) {
	cases := []struct {
		name string
		link string
		want string
	}{
		{
			name: "empty header",
			link: "",
			want: "",
		},
		{
			name: "next only",
			link: `<http://localhost:8080/ocpi/2.3.0/locations?offset=10&limit=10>; rel="next"`,
			want: "http://localhost:8080/ocpi/2.3.0/locations?offset=10&limit=10",
		},
		{
			name: "prev only",
			link: `<http://localhost:8080/ocpi/2.3.0/locations?offset=0&limit=10>; rel="prev"`,
			want: "",
		},
		{
			name: "next and prev",
			link: `<http://localhost:8080/x?offset=20&limit=10>; rel="next", <http://localhost:8080/x?offset=0&limit=10>; rel="prev"`,
			want: "http://localhost:8080/x?offset=20&limit=10",
		},
		{
			name: "malformed, no angle brackets",
			link: `rel="next"`,
			want: "",
		},
		{
			name: "rel with single quotes not matched",
			link: `<http://localhost:8080/x?offset=10>; rel='next'`,
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseNextLink(tc.link)
			if got != tc.want {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
}
