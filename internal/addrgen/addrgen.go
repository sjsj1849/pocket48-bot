// Package addrgen generates realistic random addresses for multiple countries.
// City data sourced from GeoNames (cities5000, CC-BY 4.0).
// Names and street names from public demographic data.
package addrgen

import (
	"fmt"
	"math/rand"
	"strings"
)

// Address represents a complete random identity with address.
type Address struct {
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name"`
	FullName    string `json:"full_name"`
	Street      string `json:"street"`
	StreetLine2 string `json:"street_line2"`
	City        string `json:"city"`
	State       string `json:"state"`
	Postcode    string `json:"postcode"`
	Country     string `json:"country"`
	CountryCode string `json:"country_code"`
	Phone       string `json:"phone"`
	Full        string `json:"full"`
}

// CountryDef holds address format data for one country.
type CountryDef struct {
	Name        string
	Code        string
	Cities      []CityDef
	Streets     []string
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

	// Real first + last name
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

	// Real street name + random number
	if len(def.Streets) > 0 {
		st := def.Streets[rand.Intn(len(def.Streets))]
		num := rand.Intn(9999) + 1
		addr.Street = fmt.Sprintf("%d %s", num, st)
	}

	// ~40% chance of secondary address line (Apt / Unit / Suite)
	if rand.Intn(100) < 40 {
		aptNum := rand.Intn(999) + 1
		suffixes := []string{"Apt", "Unit", "Suite"}
		if code == "JP" || code == "CN" || code == "FR" {
			suffixes = []string{"Apt", "Room"}
		}
		suf := suffixes[rand.Intn(len(suffixes))]
		addr.StreetLine2 = fmt.Sprintf("%s %d", suf, aptNum)
	}

	// Real city + state/province
	if len(def.Cities) > 0 {
		city := def.Cities[rand.Intn(len(def.Cities))]
		addr.City = city.Name
		addr.State = city.Admin1
	}

	// Realistic postcode based on country format
	addr.Postcode = genPostcode(def.PostFmt)

	// Phone with country code
	if def.PhonePrefix != "" {
		phone := "+" + def.PhonePrefix
		d := def.PhoneDigits
		switch code {
		case "US", "CA":
			// (XXX) XXX-XXXX format
			area := 0
			for i := 0; i < 3; i++ {
				area = area*10 + rand.Intn(10)
			}
			mid := 0
			for i := 0; i < 3; i++ {
				mid = mid*10 + rand.Intn(10)
			}
			last := 0
			for i := 0; i < 4; i++ {
				last = last*10 + rand.Intn(10)
			}
			phone = fmt.Sprintf("+%s (%03d) %03d-%04d", def.PhonePrefix, area, mid, last)
		case "JP":
			// 0X-XXXX-XXXX
			phone = "+" + def.PhonePrefix + " "
			for i := 0; i < d; i++ {
				if i == 1 {
					phone += "-"
				} else if i == 5 {
					phone += "-"
				}
				phone += string(rune('0' + rand.Intn(10)))
			}
		case "CN":
			// 1XX XXXX XXXX
			mobile := "1"
			mobile += string(rune('3' + rand.Intn(7))) // 13-19
			for i := 0; i < 9; i++ {
				mobile += string(rune('0' + rand.Intn(10)))
			}
			phone = fmt.Sprintf("+86 %s %s %s", mobile[:3], mobile[3:7], mobile[7:])
		case "GB":
			// 07XXX XXXXXX
			phone = "+44 07"
			for i := 0; i < 9; i++ {
				phone += string(rune('0' + rand.Intn(10)))
			}
		case "FR":
			// 06 XX XX XX XX
			phone = "+33 6"
			for i := 0; i < 8; i++ {
				if i%2 == 0 {
					phone += " "
				}
				phone += string(rune('0' + rand.Intn(10)))
			}
		default:
			for i := 0; i < d; i++ {
				if i > 0 && i%3 == 0 {
					phone += " "
				}
				phone += string(rune('0' + rand.Intn(10)))
			}
		}
		addr.Phone = phone
	}

	// Format
	addr.Full = formatAddr(addr)
	return addr, nil
}

func formatAddr(a *Address) string {
	var lines []string

	lines = append(lines, "┌─ Address ─────────────────────────────┐")
	lines = append(lines, fmt.Sprintf("  First Name: %-20s  Last Name: %s", a.FirstName, a.LastName))
	lines = append(lines, fmt.Sprintf("  Country:    %s", a.Country))

	if a.State != "" {
		if a.City != "" {
			lines = append(lines, fmt.Sprintf("  State/Prov: %-19s  City:       %s", a.State, a.City))
		} else {
			lines = append(lines, fmt.Sprintf("  State/Prov: %s", a.State))
		}
	} else if a.City != "" {
		lines = append(lines, fmt.Sprintf("  City:       %s", a.City))
	}

	lines = append(lines, fmt.Sprintf("  Address:    %s", a.Street))
	if a.StreetLine2 != "" {
		lines = append(lines, fmt.Sprintf("  Address 2:  %s", a.StreetLine2))
	}
	lines = append(lines, fmt.Sprintf("  Post Code:  %s", a.Postcode))
	if a.Phone != "" {
		lines = append(lines, fmt.Sprintf("  Phone:      %s", a.Phone))
	}
	lines = append(lines, "└────────────────────────────────────────┘")

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
