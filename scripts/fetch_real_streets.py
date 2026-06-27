#!/usr/bin/env python3
"""
Batch fetch real addresses from OpenStreetMap for all cities.
Output: Go source file with embedded real address data.
Uses: Nominatim (geocode) + Overpass API (addresses)
"""

import json, sys, time, urllib.request, urllib.parse, urllib.error, os

USER_AGENT = "pocket48-bot-address-scraper/1.0"
NOMINATIM_URL = "https://nominatim.openstreetmap.org/search"
OVERPASS_URL = "https://overpass-api.de/api/interpreter"
OUTPUT_FILE = os.path.join(os.path.dirname(__file__), "..", "internal", "addrgen", "real_addresses.go")

# All cities per country (from current data.go)
ALL_CITIES = {
    "US": [
        ("New York City","NY"),("Los Angeles","CA"),("Chicago","IL"),
        ("Houston","TX"),("Phoenix","AZ"),("Philadelphia","PA"),
        ("San Antonio","TX"),("San Diego","CA"),("Dallas","TX"),
        ("Austin","TX"),("Jacksonville","FL"),("San Jose","CA"),
        ("Fort Worth","TX"),("Columbus","OH"),("Charlotte","NC"),
        ("Indianapolis","IN"),("San Francisco","CA"),("Seattle","WA"),
        ("Denver","CO"),("Nashville","TN"),("Oklahoma City","OK"),
        ("El Paso","TX"),("Washington","DC"),("Boston","MA"),
        ("Las Vegas","NV"),("Portland","OR"),("Memphis","TN"),
        ("Louisville","KY"),("Baltimore","MD"),("Milwaukee","WI"),
        ("Albuquerque","NM"),("Tucson","AZ"),("Fresno","CA"),
        ("Sacramento","CA"),("Mesa","AZ"),("Kansas City","MO"),
        ("Atlanta","GA"),("Omaha","NE"),("Colorado Springs","CO"),
        ("Raleigh","NC"),("Miami","FL"),("Tampa","FL"),
        ("New Orleans","LA"),("Cleveland","OH"),("Honolulu","HI"),
        ("Orlando","FL"),("Buffalo","NY"),("Richmond","VA"),
        ("Birmingham","AL"),("Rochester","NY"),
    ],
    "GB": [
        ("London","ENG"),("Birmingham","ENG"),("Glasgow","SCT"),
        ("Manchester","ENG"),("Sheffield","ENG"),("Leeds","ENG"),
        ("Edinburgh","SCT"),("Liverpool","ENG"),("Bristol","ENG"),
        ("Cardiff","WLS"),("Leicester","ENG"),("Nottingham","ENG"),
        ("Oxford","ENG"),("Cambridge","ENG"),("Brighton","ENG"),
        ("York","ENG"),("Bournemouth","ENG"),("Southampton","ENG"),
        ("Portsmouth","ENG"),("Belfast","NIR"),("Aberdeen","SCT"),
        ("Newcastle upon Tyne","ENG"),("Reading","ENG"),
        ("Coventry","ENG"),("Swansea","WLS"),
    ],
    "DE": [
        ("Berlin","BE"),("Hamburg","HH"),("Munich","BY"),
        ("Cologne","NW"),("Frankfurt am Main","HE"),("Stuttgart","BW"),
        ("Duesseldorf","NW"),("Dortmund","NW"),("Essen","NW"),
        ("Dresden","SN"),("Bremen","HB"),("Hannover","NI"),
        ("Leipzig","SN"),("Nuremberg","BY"),("Bonn","NW"),
        ("Muenster","NW"),("Aachen","NW"),("Bielefeld","NW"),
        ("Mannheim","BW"),("Augsburg","BY"),
    ],
    "FR": [
        ("Paris","IDF"),("Marseille","PAC"),("Lyon","ARA"),
        ("Toulouse","OCC"),("Nice","PAC"),("Nantes","Pays"),
        ("Strasbourg","Grand"),("Bordeaux","NAQ"),("Montpellier","OCC"),
        ("Lille","HDF"),("Rennes","BRE"),("Lyon","ARA"),
        ("Toulon","PAC"),("Grenoble","ARA"),("Dijon","BFC"),
        ("Nimes","OCC"),("Angers","Pays"),("Reims","Grand"),
    ],
    "JP": [
        ("Tokyo","13"),("Yokohama","14"),("Osaka","27"),
        ("Nagoya","23"),("Sapporo","01"),("Fukuoka","40"),
        ("Kawasaki","14"),("Kobe","28"),("Kyoto","26"),
        ("Saitama","11"),("Hiroshima","34"),("Sendai","04"),
        ("Chiba","12"),("Kitakyushu","40"),("Hamamatsu","22"),
        ("Kumamoto","43"),
    ],
    "CN": [
        ("Shanghai","31"),("Beijing","11"),("Shenzhen","44"),
        ("Guangzhou","44"),("Chengdu","51"),("Tianjin","12"),
        ("Wuhan","42"),("Nanjing","32"),("Hangzhou","33"),
        ("Xi'an","61"),("Chongqing","50"),("Shenyang","21"),
        ("Dalian","21"),("Hefei","34"),("Qingdao","37"),
        ("Xiamen","35"),("Harbin","23"),("Zhengzhou","41"),
        ("Changsha","43"),("Kunming","53"),
    ],
    "AU": [
        ("Sydney","NSW"),("Melbourne","VIC"),("Brisbane","QLD"),
        ("Perth","WA"),("Adelaide","SA"),("Gold Coast","QLD"),
        ("Newcastle","NSW"),("Canberra","ACT"),("Hobart","TAS"),
        ("Darwin","NT"),("Townsville","QLD"),("Cairns","QLD"),
    ],
    "CA": [
        ("Toronto","ON"),("Montreal","QC"),("Calgary","AB"),
        ("Ottawa","ON"),("Edmonton","AB"),("Vancouver","BC"),
        ("Winnipeg","MB"),("Quebec City","QC"),("Hamilton","ON"),
        ("Mississauga","ON"),("Brampton","ON"),("Surrey","BC"),
    ],
}

COUNTRY_CODES = {"US":"us","GB":"gb","DE":"de","FR":"fr","JP":"jp","CN":"cn","AU":"au","CA":"ca"}

# Stats tracker
stats = {"total_fetched": 0, "failed_cities": []}

def geocode(city, state, country_code):
    params = {"city": city, "format": "json", "limit": 1, "countrycodes": COUNTRY_CODES[country_code]}
    if country_code == "US":
        params["state"] = state
    url = NOMINATIM_URL + "?" + urllib.parse.urlencode(params)
    req = urllib.request.Request(url)
    req.add_header("User-Agent", USER_AGENT)
    try:
        with urllib.request.urlopen(req, timeout=15) as r:
            data = json.loads(r.read())
            if data:
                return float(data[0]["lat"]), float(data[0]["lon"])
    except Exception as e:
        print(f"      Nominatim error: {e}", file=sys.stderr)
    return None, None

def fetch_addresses(lat, lon, limit=60):
    query = f'''[out:json][timeout:45];
(
  node["addr:housenumber"]["addr:street"](around:3000,{lat},{lon});
  way["addr:housenumber"]["addr:street"](around:3000,{lat},{lon});
);
out tags {limit};'''
    data = urllib.parse.urlencode({"data": query}).encode()
    req = urllib.request.Request(OVERPASS_URL, data)
    req.add_header("User-Agent", USER_AGENT)
    req.add_header("Content-Type", "application/x-www-form-urlencoded")
    try:
        with urllib.request.urlopen(req, timeout=45) as r:
            return json.loads(r.read())
    except urllib.error.HTTPError as e:
        if e.code == 429:
            print(f"      Rate limited (429), waiting 30s...", file=sys.stderr)
            time.sleep(30)
            return fetch_addresses(lat, lon, limit)  # retry once
        print(f"      Overpass error: {e.code}", file=sys.stderr)
    except Exception as e:
        print(f"      Overpass error: {e}", file=sys.stderr)
    return None

def process_city(city, state, country_code):
    print(f"  {city} ({state})...", end=" ", flush=True, file=sys.stderr)
    
    lat, lon = geocode(city, state, country_code)
    if lat is None:
        print("❌ geocode", file=sys.stderr)
        stats["failed_cities"].append(f"{country_code}/{city}")
        return []
    
    time.sleep(1.5)  # Nominatim rate limit
    
    result = fetch_addresses(lat, lon, 60)
    if not result:
        print("❌ overpass", file=sys.stderr)
        stats["failed_cities"].append(f"{country_code}/{city}")
        return []
    
    addrs = {}
    for elem in result.get("elements", []):
        t = elem.get("tags", {})
        hn = t.get("addr:housenumber", "").strip()
        st = t.get("addr:street", "").strip()
        pc = t.get("addr:postcode", "").strip()
        cc = t.get("addr:city", "").strip()
        if hn and st:
            key = f"{hn}|{st}"
            if key not in addrs:
                addrs[key] = (hn, st, pc, cc)
    
    out = sorted(addrs.values(), key=lambda x: x[1])[:50]
    stats["total_fetched"] += len(out)
    print(f"{len(out)} addr ✓", file=sys.stderr)
    return [{"h": h, "s": s, "p": p, "c": c} for h, s, p, c in out]


def generate_go_source(data):
    """Generate real_addresses.go from fetched data."""
    lines = [
        'package addrgen',
        '',
        '// RealAddress represents a real existing building address from OpenStreetMap.',
        'type RealAddress struct {',
        '\tHouseNumber string `json:"h"`',
        '\tStreet      string `json:"s"`',
        '\tPostcode    string `json:"p"`',
        '\tCityName    string `json:"c"`',
        '}',
        '',
        '// cityRealAddrs holds real addresses for a city.',
        'type cityRealAddrs struct {',
        '\tCity   string',
        '\tState  string',
        '\tAddrs  []RealAddress',
        '}',
        '',
        'var realAddresses = map[string][]cityRealAddrs{',
    ]
    
    for cc in ["US", "GB", "DE", "FR", "JP", "CN", "AU", "CA"]:
        cities_data = data.get(cc, {})
        if not cities_data:
            lines.append(f'\t"{cc}": {{}},')
            continue
        
        lines.append(f'\t"{cc}": {{')
        for city, info in sorted(cities_data.items()):
            addrs = info.get("addresses", [])
            if not addrs:
                continue
            lines.append(f'\t\t{{City: "{city}", State: "{info.get("state", "")}", Addrs: {{')
            for a in addrs:
                h = a["h"].replace("\\", "\\\\").replace('"', '\\"')
                s = a["s"].replace("\\", "\\\\").replace('"', '\\"')
                p = a.get("p", "").replace("\\", "\\\\").replace('"', '\\"')
                c = a.get("c", "").replace("\\", "\\\\").replace('"', '\\"')
                lines.append(f'\t\t\t{{HouseNumber: "{h}", Street: "{s}", Postcode: "{p}", CityName: "{c}"}},')
            lines.append('\t\t},},')
        lines.append('\t},')
    
    lines.append('}')
    lines.append('')
    return '\n'.join(lines)


def main():
    data = {}  # {cc: {city: {state, addresses: []}}}
    
    for cc in ["US", "GB", "DE", "FR", "JP", "CN", "AU", "CA"]:
        print(f"\n{'='*40}", file=sys.stderr)
        print(f"  {cc} ({len(ALL_CITIES[cc])} cities)", file=sys.stderr)
        print(f"{'='*40}", file=sys.stderr)
        data[cc] = {}
        
        for city, state in ALL_CITIES[cc]:
            addrs = process_city(city, state, cc)
            data[cc][city] = {"state": state, "addresses": addrs}
            time.sleep(1.5)  # Rate limit between cities
        
        # Extra delay between countries
        print(f"  → {cc} done, sleeping 5s...", file=sys.stderr)
        time.sleep(5)
    
    # Summary
    print(f"\n{'='*40}", file=sys.stderr)
    print(f"  DONE! Total: {stats['total_fetched']} real addresses", file=sys.stderr)
    if stats["failed_cities"]:
        print(f"  Failed: {len(stats['failed_cities'])} cities", file=sys.stderr)
        for fc in stats["failed_cities"]:
            print(f"    - {fc}", file=sys.stderr)
    
    # Generate Go code
    go_code = generate_go_source(data)
    out_path = os.path.abspath(OUTPUT_FILE)
    os.makedirs(os.path.dirname(out_path), exist_ok=True)
    with open(out_path, "w") as f:
        f.write(go_code)
    
    file_size = os.path.getsize(out_path)
    print(f"\n  Go source: {out_path} ({file_size} bytes, {go_code.count(chr(10))} lines)", file=sys.stderr)
    print(f"  Build command: cd /root/pocket48-bot && go build ./internal/addrgen/", file=sys.stderr)

    # Save canonical JSON (list format for easy human editing)
    canonical = {}
    for cc in ["US", "GB", "DE", "FR", "JP", "CN", "AU", "CA"]:
        entries = []
        for city, info in sorted(data.get(cc, {}).items()):
            addrs = info.get("addresses", [])
            if addrs:
                entries.append({"city": city, "state": info.get("state", ""), "addresses": addrs})
        if entries:
            canonical[cc] = entries
    
    json_path = os.path.join(os.path.dirname(__file__), "..", "internal", "addrgen", "real_addresses.json")
    os.makedirs(os.path.dirname(json_path), exist_ok=True)
    with open(json_path, "w", encoding="utf-8") as f:
        json.dump(canonical, f, ensure_ascii=False, indent=2)
    print(f"  Canonical JSON: {json_path} ({os.path.getsize(json_path)} bytes)", file=sys.stderr)
    
    # Also save raw per-country data for debugging
    raw_path = os.path.join(os.path.dirname(__file__), "real_addresses_raw.json")
    with open(raw_path, "w", encoding="utf-8") as f:
        json.dump(data, f, ensure_ascii=False, indent=2)
    print(f"  Raw data: {raw_path}", file=sys.stderr)
    
    # Print statistics per country
    print(f"\n{'='*40}", file=sys.stderr)
    print(f"  STATISTICS:", file=sys.stderr)
    for cc in ["US", "GB", "DE", "FR", "JP", "CN", "AU", "CA"]:
        total = sum(len(v.get("addresses", [])) for v in data.get(cc, {}).values())
        cities_with = sum(1 for v in data.get(cc, {}).values() if v.get("addresses"))
        print(f"  {cc}: {total} addresses across {cities_with} cities", file=sys.stderr)


if __name__ == "__main__":
    main()
