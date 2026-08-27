package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(Escaping{}) }

// expression carries every character two SDKs are most likely to render differently.
const expression = `total >= 0 AND note <> 'a&b' AND label <> 'naïve — "ok"'`

// query carries a comparison a JSON encoder that escapes HTML would rewrite.
const query = "SELECT id FROM orders WHERE total < 100 AND note <> 'a&b'"

// Escaping is the fixture that pins docs/ir.md §3.3: <, > and & cross the wire unescaped.
type Escaping struct{}

// Up adds a constraint and a view whose text no encoder may rewrite.
func (m Escaping) Up(ctx context.Context, s *migr.Schema) error {
	if err := s.Table("orders").
		AddCheck("ck_orders_sane", expression).
		Apply(ctx); err != nil {
		return err
	}

	return s.View("small_orders").Create(query).Apply(ctx)
}

// Down removes both, recording the same text a second time.
func (m Escaping) Down(ctx context.Context, s *migr.Schema) error {
	if err := s.View("small_orders").Drop().Was(query).Apply(ctx); err != nil {
		return err
	}

	return s.Table("orders").
		DropCheck("ck_orders_sane").Was(migr.CheckDef{Expression: expression}).
		Apply(ctx)
}
