package wolverine_test

import (
	"context"
	"github.com/autom8ter/wolverine"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestSchema(t *testing.T) {
	usrBytes, err := wolverine.UserCollection.MarshalJSON()
	assert.Nil(t, err)
	taskBytes, err := wolverine.TaskCollection.MarshalJSON()
	assert.Nil(t, err)
	t.Run("newJSONSchema", func(t *testing.T) {
		_, err := wolverine.NewJSONSchema(usrBytes)
		assert.Nil(t, err)
		_, err = wolverine.NewJSONSchema(taskBytes)
		assert.Nil(t, err)
	})
	t.Run("validate", func(t *testing.T) {
		s, err := wolverine.NewJSONSchema(usrBytes)
		assert.Nil(t, err)
		assert.Nil(t, s.Validate(context.Background(), []byte(wolverine.JSONString(wolverine.NewUserDoc()))))

		s, err = wolverine.NewJSONSchema(taskBytes)
		assert.Nil(t, err)
		assert.Nil(t, s.Validate(context.Background(), []byte(wolverine.JSONString(wolverine.NewTaskDoc("1")))))
	})
	t.Run("config", func(t *testing.T) {
		s, err := wolverine.NewJSONSchema(usrBytes)
		assert.Nil(t, err)
		assert.NotEmpty(t, s.PrimaryKey)
		assert.EqualValues(t, "_id", s.PrimaryKey())
		assert.NotEmpty(t, s.Collection())
		assert.NotEmpty(t, s.Indexing().Indexes)

		s, err = wolverine.NewJSONSchema(taskBytes)
		assert.Nil(t, err)
		assert.NotEmpty(t, s.PrimaryKey())
		assert.NotEmpty(t, s.Collection())
	})
}
