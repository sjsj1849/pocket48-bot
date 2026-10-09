package hearts2hearts

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Term struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Note   string `json:"note,omitempty"`
}

type Glossary struct {
	Context string `json:"context"`
	Terms   []Term `json:"terms"`
}

func Default() Glossary {
	return Glossary{
		Context: "内容来自韩国女团 Hearts2Hearts 成员的实时聊天，可能包含成员姓名、粉丝名、歌曲、专辑、品牌代言、团内外号、饭圈梗和承接上文的省略句。成员代表 emoji：JIWOO🍓、CARMEN🌴、YUHA🎀、STELLA🧁、JUUN👾、A-NA🌻、IAN🫛、YE-ON😊。",
		Terms: []Term{
			{Source: "태자비들", Target: "太子妃们", Note: "粉丝自称"},
			{Source: "태자비", Target: "太子妃", Note: "粉丝自称"},
			{Source: "태자님", Target: "太子殿下", Note: "伊安的外号"},
			{Source: "하츄들", Target: "哈啾们", Note: "Hearts2Hearts 粉丝 S2U"},
			{Source: "하츄", Target: "哈啾", Note: "Hearts2Hearts 粉丝 S2U"},
			{Source: "하츠투하츠", Target: "Hearts2Hearts", Note: "团名"},
			{Source: "스또삐", Target: "STTOPPI", Note: "STELLA 的低频外号，由 YUHA 所起"},
			{Source: "sttoppi", Target: "STTOPPI", Note: "STELLA 的低频外号，由 YUHA 所起"},
			{Source: "정이안", Target: "郑伊安", Note: "成员 IAN 本名"},
			{Source: "정광", Target: "郑狂", Note: "IAN 的低频团内外号"},
			{Source: "최광", Target: "崔狂", Note: "团综中与“郑狂”相关的原称呼"},
			{Source: "이안", Target: "伊安", Note: "成员 IAN"},
			{Source: "개인라방", Target: "个人直播"},
			{Source: "라방", Target: "直播"},
			{Source: "컴백 스포", Target: "回归剧透"},
			{Source: "양갈래", Target: "双马尾"},
			{Source: "곤룡포", Target: "龙袍"},
			{Source: "투에이엔", Target: "2aN", Note: "化妆品品牌"},
		},
	}
}

func Path(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "storage", "hearts2hearts", "glossary.json")
}

func Load(configPath string) (Glossary, error) {
	b, err := os.ReadFile(Path(configPath))
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Glossary{}, err
	}
	var glossary Glossary
	if err := json.Unmarshal(b, &glossary); err != nil {
		return Glossary{}, err
	}
	if err := Validate(glossary); err != nil {
		return Glossary{}, err
	}
	return glossary, nil
}

func Save(configPath string, glossary Glossary) error {
	if err := Validate(glossary); err != nil {
		return err
	}
	path := Path(configPath)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(glossary, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".glossary-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func Validate(glossary Glossary) error {
	if len([]rune(glossary.Context)) > 2000 {
		return fmt.Errorf("团体背景不能超过 2000 个字符")
	}
	if len(glossary.Terms) > 300 {
		return fmt.Errorf("术语不能超过 300 条")
	}
	seen := map[string]bool{}
	for i, term := range glossary.Terms {
		source, target := strings.TrimSpace(term.Source), strings.TrimSpace(term.Target)
		if source == "" || target == "" {
			return fmt.Errorf("第 %d 条术语的原词和译法不能为空", i+1)
		}
		if len([]rune(source)) > 100 || len([]rune(target)) > 150 || len([]rune(term.Note)) > 200 {
			return fmt.Errorf("第 %d 条术语过长", i+1)
		}
		key := strings.ToLower(source)
		if seen[key] {
			return fmt.Errorf("术语 %q 重复", source)
		}
		seen[key] = true
	}
	return nil
}

func (g Glossary) normalizedTerms() []Term {
	terms := append([]Term(nil), g.Terms...)
	for i := range terms {
		terms[i].Source = strings.TrimSpace(terms[i].Source)
		terms[i].Target = strings.TrimSpace(terms[i].Target)
		terms[i].Note = strings.TrimSpace(terms[i].Note)
	}
	sort.SliceStable(terms, func(i, j int) bool {
		return len([]rune(terms[i].Source)) > len([]rune(terms[j].Source))
	})
	return terms
}

// Apply locks known Hearts2Hearts terms before machine translation. Existing
// Chinese is intentionally left in the source so the model only translates
// the surrounding Korean grammar.
func (g Glossary) Apply(source string) string {
	for _, term := range g.normalizedTerms() {
		source = strings.ReplaceAll(source, term.Source, term.Target)
	}
	return source
}

func (g Glossary) Instructions() string {
	var b strings.Builder
	if context := strings.TrimSpace(g.Context); context != "" {
		b.WriteString("固定语境：")
		b.WriteString(context)
		b.WriteByte('\n')
	}
	if len(g.Terms) > 0 {
		b.WriteString("共享术语表（出现时必须采用指定译法）：\n")
		for _, term := range g.normalizedTerms() {
			fmt.Fprintf(&b, "- %s => %s", term.Source, term.Target)
			if term.Note != "" {
				fmt.Fprintf(&b, "（%s）", term.Note)
			}
			b.WriteByte('\n')
		}
	}
	return strings.TrimSpace(b.String())
}
