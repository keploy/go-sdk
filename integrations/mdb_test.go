package integrations

import (
	// "encoding/json"
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"testing"

	"github.com/go-test/deep"
	"github.com/keploy/go-sdk/keploy"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.uber.org/zap"
)

type Trainer struct {
	Name string
	Age  int
	City string
}

func TestMDBFindOne(t testing.T){
	r01 := make([]byte, 0)
	r01 = append(r01, 1)
	var r11 bytes.Buffer        
	enc := gob.NewEncoder(&r11) 
	obj := Trainer{
		Name: "Ash",
		Age: 10,
		City: "Pallet Town",
	}
	err := enc.Encode(obj)
	if err != nil {
		fmt.Println(err.Error())
	}
	r21 := make([]byte, 0)
	r21 = append(r21, 1)
	ctx1,_ :=context.WithCancel(context.Background())
	ctx1 = context.WithValue(ctx1, keploy.KCTX, &keploy.Context{
		Mode:   "test",
		TestID: "8f7f6705-87eb-4c56-a096-85ba47071080",
		Deps:   map[string][]keploy.Dependency{
			keploy.Dependency{
				"name": "",
				"type": "",
				"meta": map[string]string{
					"name": "ritikApp",
					"type": "DB",
					"operation": "FindOne.Err",
				},
				"data": [][]byte{
					r01,
				},
			},
			keploy.Dependency{
				"name": "",
				"type": "",
				"meta": map[string]string{
					"name": "ritikApp",
					"type": "DB",
					"operation": "FindOne.Decode",
				},
				"data": [][]byte{
					r11,
					r21,
				},
			},
		},
	})
	for _, tt := range []struct {
		ctx context.Context
		filter bson.M
		result Trainer
	}{
		{
			ctx: ctx1,
			filter: bson.M{"name": "Ash"},
			result: Trainer{
				Name: "Ash",
				Age: 10,
				City: "Pallet Town",
			},
			err: nil,
		},
	}{}
}