package integrations

import (
	// "encoding/json"
	// "bytes"
	"context"
	// "encoding/gob"
	"errors"
	"fmt"
	"log"
	"testing"

	// "github.com/go-test/deep"
	"github.com/keploy/go-sdk/keploy"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	// "go.uber.org/zap"
)

type Trainer struct {
	Name string
	Age  int
	City string
}

func connect() *MongoDB{
	clientOptions := options.Client().ApplyURI("mongodb+srv://admin:zYLnsfk29c770IE1@keploy-test.mujuh.mongodb.net/test?retryWrites=true&w=majority")
	client, err := mongo.Connect(context.TODO(), clientOptions)
	if err != nil {
		log.Fatal(err)
	}
	// Check the connection
	err = client.Ping(context.TODO(), nil)
	if err != nil {
		log.Fatal(err)
	}
	return NewMongoDB(client.Database("test").Collection("client"))
}

func TestFindOne(t *testing.T){
	collection := connect()

	//test for findOne method
	for index, tt := range []struct {
		ctx context.Context
		filter bson.M
		result Trainer
		err error
	}{
		// test mode document present in client DB
		{
			ctx: context.WithValue(context.TODO(), keploy.KCTX, &keploy.Context{
				Mode:   "test",
				TestID: "8f7f6705-87eb-4c56-a096-85ba47071080",
				Deps:   []keploy.Dependency{
							{
								Name: "",
								Type: "",
								Meta: map[string]string{
									"name": "ritikApp",
									"type": "DB",
									"operation": "FindOne.Decode",
								},
								Data: [][]byte{
									{47, 255, 133, 3, 1, 1, 7, 84, 114, 97, 105, 110, 101, 114, 1, 255, 134, 0, 1, 3, 1, 4, 78, 97, 109, 101, 1, 12, 0, 1, 3, 65, 103, 101, 1, 4, 0, 1, 4, 67, 105, 116, 121, 1, 12, 0, 0, 0, 23, 255, 134, 1, 3, 65, 115, 104, 1, 20, 1, 11, 80, 97, 108, 108, 101, 116, 32, 84, 111, 119, 110, 0},
									{10, 255, 129, 5, 1, 2, 255, 132, 0, 0, 0, 5, 255, 130, 0, 1, 1},
								},
							},
						},
			}),
			filter: bson.M{"name": "Ash"},
			result: Trainer{
				Name: "Ash",
				Age: 10,
				City: "Pallet Town",
			},
			err: nil,
		},
		// test mode document not present in client DB
		{
			ctx: context.WithValue(context.TODO(), keploy.KCTX, &keploy.Context{
				Mode:   "test",
				TestID: "8f7f6705-87eb-4c56-a096-85ba47071080",
				Deps:   []keploy.Dependency{
							{
								Name: "",
								Type: "",
								Meta: map[string]string{
									"name": "ritikApp",
									"type": "DB",
									"operation": "FindOne.Decode",
								},
								Data: [][]byte{
									{47, 255, 133, 3, 1, 1, 7, 84, 114, 97, 105, 110, 101, 114, 1, 255, 134, 0, 1, 3, 1, 4, 78, 97, 109, 101, 1, 12, 0, 1, 3, 65, 103, 101, 1, 4, 0, 1, 4, 67, 105, 116, 121, 1, 12, 0, 0, 0, 3, 255, 134, 0},
									{10, 255, 129, 5, 1, 2, 255, 132, 0, 0, 0, 34, 255, 130, 0, 30, 1, 109, 111, 110, 103, 111, 58, 32, 110, 111, 32, 100, 111, 99, 117, 109, 101, 110, 116, 115, 32, 105, 110, 32, 114, 101, 115, 117, 108, 116},
								},
							},
						},
			}),
			filter: bson.M{"name": "Jain"},
			result: Trainer{
				Name: "",
				Age: 0,
				City: "",
			},
			err: errors.New("mongo: no documents in result"),
		},
		// capture mode document present in client DB
		{
			ctx: context.WithValue(context.TODO(), keploy.KCTX, &keploy.Context{
				Mode: "capture",
				TestID: "",
				Deps: []keploy.Dependency{},
			}),
			filter: bson.M{"name": "Ash"},
			result: Trainer{
				Name: "Ash",
				Age: 10,
				City: "Pallet Town",
			},
			err: nil,
		},
		// capture mode document not present in client DB
		{
			ctx: context.WithValue(context.TODO(), keploy.KCTX, &keploy.Context{
				Mode: "capture",
				TestID: "",
				Deps: []keploy.Dependency{},
			}),
			filter: bson.M{"name": "Jain"},
			result: Trainer{
				Name: "",
				Age: 0,
				City: "",
			},
			err: errors.New("mongo: no documents in result"),
		},
		//not in a valid SDK mode
		{
			ctx : context.WithValue(context.TODO(), keploy.KCTX, &keploy.Context{
				Mode: "XYZ",
				TestID: "",
				Deps: []keploy.Dependency{},
			}),
			filter: bson.M{"name": "Ash"},
			result: Trainer{},
			err: errors.New("integrations: Not in a valid sdk mode"),
		},
		//keploy context not present
		{
			ctx: context.TODO(),
			filter: bson.M{"name": "Ash"},
			result: Trainer{},
			err: errors.New("failed to get Keploy context"),
		},
	}{
		var res Trainer = Trainer{}
		eRr := collection.FindOne(tt.ctx, tt.filter).Decode(&res)
		
		//compare returned and expected error
		if (eRr!=nil && tt.err==nil) || (eRr==nil && tt.err!=nil) || (eRr!=nil && tt.err!=nil && eRr.Error()!=tt.err.Error()) {
			log.Fatal(" Testcase ", index," failed in error \n ", tt.ctx, tt.filter,"\n   ", tt.err,"\n   ", eRr )
		}

		//compare returned and expected output
		if res.Name==tt.result.Name || res.Age==tt.result.Age || res.City==tt.result.City{
			fmt.Printf(" Testcase %v Passed\n", index)
		}else{
			log.Fatal(" Testcase ", index," failed in result \n ", tt.ctx, tt.filter,"\n   ", tt.result,"\n   ", res, )
		}
	}
}

func TestInsertOne (t *testing.T){
	collection := connect()

	// test for insertOne method
	iid,_ := primitive.ObjectIDFromHex( "61c9a42529e6c1d66aecc955" )
	for index,ti := range []struct{
		ctx context.Context
		document Trainer
		result *mongo.InsertOneResult
		err error
	}{
		//test mode insertOne successful
		{
			ctx: context.WithValue(context.TODO(), keploy.KCTX, &keploy.Context{
				Mode: "test",
				TestID: "8f7f6705-87eb-4c56-a096-85ba47071080",
				Deps: []keploy.Dependency{
					{
						Name: "",
						Type: "",
						Meta: map[string]string{
							"name": "ritikApp",
							"type": "DB",
							"operation": "InsertOne",
						},
						Data: [][]byte{
							{44, 255, 129, 3, 1, 1, 15, 73, 110, 115, 101, 114, 116, 79, 110, 101, 82, 101, 115, 117, 108, 116, 1, 255, 130, 0, 1, 1, 1, 10, 73, 110, 115, 101, 114, 116, 101, 100, 73, 68, 1, 16, 0, 0, 0, 79, 255, 130, 1, 51, 103, 111, 46, 109, 111, 110, 103, 111, 100, 98, 46, 111, 114, 103, 47, 109, 111, 110, 103, 111, 45, 100, 114, 105, 118, 101, 114, 47, 98, 115, 111, 110, 47, 112, 114, 105, 109, 105, 116, 105, 118, 101, 46, 79, 98, 106, 101, 99, 116, 73, 68, 255, 131, 1, 1, 1, 8, 79, 98, 106, 101, 99, 116, 73, 68, 1, 255, 132, 0, 1, 6, 1, 24, 0, 0, 25, 255, 132, 21, 0, 12, 97, 255, 201, 255, 164, 37, 41, 255, 230, 255, 193, 255, 214, 106, 255, 236, 255, 201, 85, 0},
							{10, 255, 133, 5, 1, 2, 255, 136, 0, 0, 0, 5, 255, 134, 0, 1, 1},
						},
					},
				},
			}),
			document: Trainer{
				Name: "Ash",
				Age: 10,
				City: "Pallet Town",
			},
			result: &mongo.InsertOneResult{
				InsertedID: iid,
			},
			err: nil,
		},
		//capture mode 
		{
			ctx: context.WithValue(context.TODO(), keploy.KCTX, &keploy.Context{
				Mode: "capture",
				TestID: "",
				Deps: []keploy.Dependency{},
			}),
			document: Trainer{
				Name: "Brock",
				Age: 15,
				City: "Pewter City",
			},
			result: &mongo.InsertOneResult{},
			err: nil,
		},
		//not in a valid mode
		{
			ctx: context.WithValue(context.TODO(), keploy.KCTX, &keploy.Context{
				Mode: "XYZ",
				TestID: "",
				Deps: []keploy.Dependency{},
			}),
			document: Trainer{
				Name: "Brock",
				Age: 15,
				City: "Pewter City",
			},
			result: nil,
			err : errors.New("integrations: Not in a valid sdk mode"),
		},
		//keploy context not present
		{
			ctx: context.TODO(),
			document: Trainer{
				Name: "Brock",
				Age: 15,
				City: "Pewter City",
			},
			result: nil,
			err : errors.New("failed to get Keploy context"),
		},
	}{
		res, eRr := collection.InsertOne(ti.ctx, ti.document)
		
		//compare returned error and expected error
		if (eRr!=nil && ti.err==nil) || (eRr==nil && ti.err!=nil) || (eRr!=nil && ti.err!=nil && eRr.Error()!=ti.err.Error()) {
			log.Fatal(" Testcase ", index," failed in error \n ", ti.ctx, ti.document,"\n   ", ti.err,"\n   ", eRr )
		}

		d := ti.ctx.Value(keploy.KCTX)
		deps,ok := d.(*keploy.Context)
		
		//compare returned output and expected output only in test mode
		if !ok || deps.Mode!="test" || ( deps.Mode=="test" && 
			res.InsertedID.(primitive.ObjectID).Hex() == ti.result.InsertedID.(primitive.ObjectID).Hex()){
			
			fmt.Printf(" Testcase %v Passed\n", index)
		
		}else{
			log.Fatal(" Testcase ", index," failed in result \n ", ti.ctx, ti.document,"\n   ", ti.result,"\n   ", res, )
		}
	}
}