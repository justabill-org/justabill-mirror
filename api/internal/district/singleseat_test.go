package district_test

import (
	"context"
	"testing"

	"github.com/justabill-org/justabill/api/internal/district"
)

// noMatch is the geocoder's answer for an address it can't match.
const noMatch = `{"result":{"addressMatches":[]}}`

func TestFromAddress_SingleSeatFallback(t *testing.T) {
	tests := []struct {
		address string
		want    string // the single-seat state, or "" for no result
	}{
		// Trailing "ST 12345", in every single-seat jurisdiction.
		{"…, Hagåtña, GU 96910", "GU"},
		{"…, Juneau, AK 99801", "AK"},
		{"411 Legislative Ave, Dover, DE 19901", "DE"},
		{"600 E Boulevard Ave, Bismarck, ND 58505-0001", "ND"},
		{"500 E Capitol Ave, Pierre, SD 57501", "SD"},
		{"115 State St, Montpelier, VT 05633", "VT"},
		{"200 W 24th St, Cheyenne, WY 82001", "WY"},
		{"1600 Pennsylvania Ave NW, Washington, D.C. 20500", "DC"},
		{"1 Calle Fortaleza, San Juan, PR 00901", "PR"},
		{"5 Norre Gade, Charlotte Amalie, VI 00802", "VI"},
		{"Fagatogo, AS 96799", "AS"},
		{"Capitol Hill, Saipan, MP 96950", "MP"},
		// Lower case, no ZIP, or the jurisdiction's name.
		{"1 marine corps dr, hagatna, gu", "GU"},
		{"…, Juneau, Alaska 99801", "AK"},
		{"…, Charlotte Amalie, U.S. Virgin Islands 00802", "VI"},
		{"…, Saipan, Northern Mariana Islands", "MP"},
		{"…, Pierre, South Dakota", "SD"},
		// No state: the Island Area ZIP codes.
		{"…, Pago Pago 96799", "AS"},
		{"1 Marine Corps Dr, Hagatna 96910", "GU"},
		{"Tamuning 96932", "GU"},
		{"Saipan 96950", "MP"},
		{"Tinian 96952", "MP"},
		{"Christiansted 00820", "VI"},
		{"Charlotte Amalie 00802-1234", "VI"},
		// Multi-district states, and what the fallback can't place.
		{"…, Austin, TX 78701", ""},
		{"…, Austin, Texas 78701", ""},
		{"…, Portland, ME 04101", ""},
		{"…, Austin, TX 96910", ""}, // the state text wins over the ZIP
		{"…, Koror 96940", ""},      // Palau: a 969 ZIP with no seat in Congress
		{"…, Majuro 96960", ""},     // Marshall Islands
		{"…, Pohnpei 96941", ""},    // Micronesia
		{"…, Hagatna, GU 9691", ""}, // a malformed ZIP: no trailing "ST 12345"
		{"somewhere 9691", ""},      // not a ZIP
		{"1 Main St, Springfield 62701", ""},
		{"96910 Main St, Springfield", ""}, // a ZIP-like house number isn't a ZIP
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			g := newGeocoder(t, noMatch)

			got, err := g.lookup().FromAddress(context.Background(), tt.address, 119)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.want == "" {
				if len(got) != 0 {
					t.Errorf("got %+v, want no result", got)
				}
				return
			}
			want := district.Result{State: tt.want, District: 0, AtLarge: true, Source: district.SourceState}
			if len(got) != 1 || got[0] != want {
				t.Errorf("got %+v, want %+v", got, want)
			}
		})
	}
}

// The fallback reads the address locally: the geocoder is the only request, and it's made once.
func TestFromAddress_SingleSeatFallbackSendsNothing(t *testing.T) {
	g := newGeocoder(t, noMatch)

	if _, err := g.lookup().FromAddress(context.Background(), "…, Pago Pago 96799", 119); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(g.queries) != 1 {
		t.Errorf("requests = %d, want only the geocoder's 1", len(g.queries))
	}
}

func TestFromAddress_GeocoderMatchSkipsFallback(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []district.Result
	}{
		// A geocoder answer in an at-large state stays the geocoder's.
		{"at-large match", oneRecord(layer119, "0200", "119"),
			[]district.Result{{State: "AK", District: 0, AtLarge: true, Source: district.SourceGeocoder}}},
		// Even when the text names a single-seat state, the geocoder's district wins.
		{"district match", oneRecord(layer119, "4837", "119"),
			[]district.Result{{State: "TX", District: 37, Source: district.SourceGeocoder}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newGeocoder(t, tt.body)

			got, err := g.lookup().FromAddress(context.Background(), "…, Juneau, AK 99801", 119)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) || got[0] != tt.want[0] {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// A layer error isn't a missing match: the fallback doesn't hide it.
func TestFromAddress_SingleSeatFallbackKeepsLayerErrors(t *testing.T) {
	g := newGeocoder(t, oneRecord("118th Congressional Districts", "6698", "118"))

	if _, err := g.lookup().FromAddress(context.Background(), "…, Hagåtña, GU 96910", 119); err == nil {
		t.Error("expected an unexpected-layer error, got none")
	}
}
