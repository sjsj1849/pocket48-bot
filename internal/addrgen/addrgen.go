// Package addrgen generates realistic random addresses for multiple countries.
// City data sourced from GeoNames (cities5000, CC-BY 4.0).
// Real addresses sourced from OpenStreetMap building data.
// Names and street names from public demographic data.
package addrgen

import (
	"fmt"
	"math/rand"
	"strings"
)

// RealAddress represents a real existing building address from OpenStreetMap.
type RealAddress struct {
	HouseNumber string `json:"h"`
	Street      string `json:"s"`
	Postcode    string `json:"p"`
	CityName    string `json:"c"`
}

// cityRealAddrs holds real addresses for a city.
type cityRealAddrs struct {
	City  string
	State string
	Addrs []RealAddress
}

// realAddresses maps country code -> cities with real address data.
// Populated by generated real_addresses.go (from OSM data).
// This empty declaration is replaced when real_addresses.go is generated.
var realAddresses = map[string][]cityRealAddrs{}

// Address represents a complete random identity with address.
type Address struct {
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name"`
	FullName    string `json:"full_name"`
	Street      string `json:"street"`       // full street line: "123 Main St"
	StreetLine2 string `json:"street_line2"` // optional: "Apt 4B"
	City        string `json:"city"`
	State       string `json:"state"`
	Postcode    string `json:"postcode"`
	Country     string `json:"country"`
	CountryCode string `json:"country_code"`
	Phone       string `json:"phone"`
	Full        string `json:"full"` // formatted box output
}

// CountryDef holds address format data for one country (fallback + names).
type CountryDef struct {
	Name        string
	Code        string
	Cities      []CityDef   // fallback cities (used when no real addresses)
	Streets     []string    // fallback country-wide streets
	FirstNames  []string
	LastNames   []string
	PostFmt     string
	PhonePrefix string
	PhoneDigits int
}

// CityDef holds city name + admin code (state/province).
type CityDef struct {
	Name   string
	Admin1 string
}

// Generator holds country data.
type Generator struct {
	countries map[string]*CountryDef
}

// New creates a Generator with built-in country data.
func New() *Generator {
	g := &Generator{countries: make(map[string]*CountryDef)}
	initData(g)
	return g
}

// Countries returns the list of supported country codes.
func (g *Generator) Countries() []string {
	var out []string
	for _, c := range g.countries {
		out = append(out, fmt.Sprintf("%s (%s)", c.Code, c.Name))
	}
	return out
}

// Generate creates a random address for the given ISO country code.
// Uses real OSM addresses when available; falls back to random generation.
func (g *Generator) Generate(countryCode string) (*Address, error) {
	code := strings.ToUpper(strings.TrimSpace(countryCode))
	def, ok := g.countries[code]
	if !ok {
		return nil, fmt.Errorf("unsupported country: %s", code)
	}

	addr := &Address{
		Country:     def.Name,
		CountryCode: def.Code,
	}

	// ---- Name (always real from curated pools) ----
	if len(def.FirstNames) > 0 {
		addr.FirstName = def.FirstNames[rand.Intn(len(def.FirstNames))]
	}
	if len(def.LastNames) > 0 {
		addr.LastName = def.LastNames[rand.Intn(len(def.LastNames))]
	}
	if addr.FirstName != "" && addr.LastName != "" {
		if code == "JP" || code == "CN" {
			addr.FullName = addr.LastName + " " + addr.FirstName
		} else {
			addr.FullName = addr.FirstName + " " + addr.LastName
		}
	}

	// ---- Address: try real OSM data first ----
	if usedRealAddr := tryRealAddress(code, addr); !usedRealAddr {
		// Fallback: random combination
		if len(def.Streets) > 0 {
			st := def.Streets[rand.Intn(len(def.Streets))]
			num := rand.Intn(9999) + 1
			addr.Street = fmt.Sprintf("%d %s", num, st)
		}
		if len(def.Cities) > 0 {
			city := def.Cities[rand.Intn(len(def.Cities))]
			addr.City = city.Name
			addr.State = city.Admin1
		}
		addr.Postcode = genPostcode(def.PostFmt)
	}

	// ---- Address line only (no fake second line) ----

	// ---- Phone ----
	if def.PhonePrefix != "" {
		addr.Phone = genPhone(code, def.PhonePrefix, def.PhoneDigits)
	}

	// ---- Format ----
	addr.Full = formatAddr(addr)
	return addr, nil
}

// tryRealAddress attempts to fill address fields from real OSM data.
// Returns true if a real address was used.
func tryRealAddress(code string, addr *Address) bool {
	cities, ok := realAddresses[code]
	if !ok || len(cities) == 0 {
		return false
	}

	// Collect cities that have real addresses
	var eligible []cityRealAddrs
	for _, c := range cities {
		if len(c.Addrs) > 0 {
			eligible = append(eligible, c)
		}
	}
	if len(eligible) == 0 {
		return false
	}

	// Pick random city from eligible, then random address
	chosen := eligible[rand.Intn(len(eligible))]
	ra := chosen.Addrs[rand.Intn(len(chosen.Addrs))]

	addr.City = chosen.City
	addr.State = chosen.State
	addr.Street = fmt.Sprintf("%s %s", ra.HouseNumber, ra.Street)
	addr.Postcode = ra.Postcode

	return true
}

func genPhone(code, prefix string, digits int) string {
	phone := "+" + prefix
	switch code {
	case "US", "CA":
		area := rand.Intn(1000)
		mid := rand.Intn(1000)
		last := rand.Intn(10000)
		return fmt.Sprintf("+%s (%03d) %03d-%04d", prefix, area, mid, last)
	case "JP":
		phone += " "
		for i := 0; i < digits; i++ {
			if i == 1 || i == 5 {
				phone += "-"
			}
			phone += string(rune('0' + rand.Intn(10)))
		}
		return phone
	case "CN":
		mobile := "1" + string(rune('3'+rand.Intn(7)))
		for i := 0; i < 9; i++ {
			mobile += string(rune('0' + rand.Intn(10)))
		}
		return fmt.Sprintf("+86 %s %s %s", mobile[:3], mobile[3:7], mobile[7:])
	case "GB":
		phone += " 07"
		for i := 0; i < 9; i++ {
			phone += string(rune('0' + rand.Intn(10)))
		}
		return phone
	case "FR":
		phone += " 6"
		for i := 0; i < 8; i++ {
			if i%2 == 0 {
				phone += " "
			}
			phone += string(rune('0' + rand.Intn(10)))
		}
		return phone
	default:
		for i := 0; i < digits; i++ {
			if i > 0 && i%3 == 0 {
				phone += " "
			}
			phone += string(rune('0' + rand.Intn(10)))
		}
		return phone
	}
}

func formatAddr(a *Address) string {
	var lines []string

	lines = append(lines, "📍 随机地址生成")
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("  姓名:       %s", a.FullName))
	lines = append(lines, fmt.Sprintf("  国家:       %s", a.Country))

	if a.State != "" && a.City != "" {
		lines = append(lines, fmt.Sprintf("  州省:       %s", a.State))
		lines = append(lines, fmt.Sprintf("  城市:       %s", a.City))
	} else if a.City != "" {
		lines = append(lines, fmt.Sprintf("  城市:       %s", a.City))
	}

	lines = append(lines, fmt.Sprintf("  地址:       %s", a.Street))
	lines = append(lines, fmt.Sprintf("  邮编:       %s", a.Postcode))
	if a.Phone != "" {
		lines = append(lines, fmt.Sprintf("  电话:       %s", a.Phone))
	}

	return strings.Join(lines, "\n")
}

func genPostcode(fmtStr string) string {
	if fmtStr == "" {
		fmtStr = "#####"
	}
	var out strings.Builder
	for _, ch := range fmtStr {
		switch ch {
		case '#':
			out.WriteRune(rune('0' + rand.Intn(10)))
		case 'A':
			out.WriteRune(rune('A' + rand.Intn(26)))
		case 'a':
			out.WriteRune(rune('a' + rand.Intn(26)))
		default:
			out.WriteRune(ch)
		}
	}
	return out.String()
}
