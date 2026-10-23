package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(CreateBooks{}) }

// CreateBooks adds the books table, each book naming the author the SQL migration before it created.
type CreateBooks struct{}

// Up creates the table with its key inline, then the key to authors.
func (CreateBooks) Up(ctx context.Context, s *migr.Schema) error {
	if err := s.CreateTable("books").
		Column("id", migr.Int64()).
		Column("author_id", migr.Int64()).
		Column("title", migr.String(200)).
		PrimaryKey("id").
		Apply(ctx); err != nil {
		return err
	}

	return s.Table("books").
		AddForeignKey("fk_books_author", []string{"author_id"}, "authors", []string{"id"}).
		Apply(ctx)
}
