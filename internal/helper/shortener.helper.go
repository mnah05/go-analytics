package helper

import (
	"math/rand"

	"github.com/sqids/sqids-go"
)

// NewShortener returns a new sqids encoder with a shuffled alphabet seeded by salt.
func NewShortener(salt uint64) *sqids.Sqids {
	alphabet := []rune("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

	r := rand.New(rand.NewSource(int64(salt)))
	r.Shuffle(len(alphabet), func(i, j int) {
		alphabet[i], alphabet[j] = alphabet[j], alphabet[i]
	})
	s, _ := sqids.New(sqids.Options{
		Alphabet:  string(alphabet),
		MinLength: 11,
	})
	return s
}

func GenerateSlug(s *sqids.Sqids, linkID int64) string {
	slug, _ := s.Encode([]uint64{uint64(linkID)})
	return slug
}
