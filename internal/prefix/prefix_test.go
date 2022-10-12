package prefix

import (
	"fmt"
	"strings"
	"testing"

	"github.com/brianvoe/gofakeit/v6"
	"github.com/stretchr/testify/assert"
)

func TestPrefix(t *testing.T) {
	t.Run("prefix", func(t *testing.T) {
		id := gofakeit.UUID()
		p := NewPrefixedIndex("user", []string{"_id"})
		prefix := p.GetIndex(id, map[string]any{
			"_id": id,
			"age": 15,
		})
		t.Log(prefix)
		assert.True(t, strings.HasPrefix(prefix, fmt.Sprintf(`indexuser"_id"%s`, toStringHash(id))))
	})

}
