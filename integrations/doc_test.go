package integrations_test

import (
	"context"
	"fmt"
	"log"
	"time"
	"google.golang.org/grpc"
	"github.com/bnkamalesh/webgo/v4"
	"github.com/keploy/go-sdk/integrations"
	"github.com/keploy/go-sdk/keploy"
	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)
func ExampleNewMongoCollection(){
	clientOptions := options.Client().ApplyURI("mongodb://localhost:27017")
	client, err := mongo.Connect(context.TODO(), clientOptions)
	if err != nil {
		log.Fatal(err)
	}

	// Check the connection
	err = client.Ping(context.TODO(), nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Connected to MongoDB!")
	var collection *integrations.MongoCollection
	result, err := collection.InsertOne(context.TODO(), bson.D{{"x", 1}})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("inserted ID: %v\n", result.InsertedID)
}

func ExampleMongoSingleResult_Err(){
		var(
			sr 		   *integrations.MongoSingleResult
			collection *integrations.MongoCollection
		)
		filter := bson.M{"name": "Ash"}
		findOneOpts := options.FindOne()
		findOneOpts.SetComment("this is cool stuff")

		sr = collection.FindOne(context.TODO(), filter, findOneOpts)
		err := sr.Err()
		if err!=nil{
			log.Fatal(err)
		}
}

func ExampleMongoSingleResult_Decode(){
		var(
			sr 		   *integrations.MongoSingleResult
			collection *integrations.MongoCollection
		)
		filter := bson.M{"name": "Ash"}
		var result bson.D
		findOneOpts := options.FindOne()
		findOneOpts.SetComment("this is cool stuff")

		sr = collection.FindOne(context.TODO(), filter, findOneOpts)
		err := sr.Decode(&result)
		if err!=nil{
			log.Fatal(err)
		}
		fmt.Printf("found document: %v", fmt.Sprint(result))
}

func ExampleMongoCursor_Err(){
		var(
			collection *integrations.MongoCollection
			cur 	   *integrations.MongoCursor
			err 		error
		)
		filter := bson.M{"name": "Misty"}
		findOpts := options.Find()
		findOpts.SetSort(bson.D{{"age", -1}})
		cur,_ = collection.Find(context.TODO(), filter, findOpts)
		err = cur.Err()
		if err!=nil{
			log.Fatal(err)
		}
}

func ExampleMongoCursor_Next(){
		var(
			collection *integrations.MongoCollection
			cur 	   *integrations.MongoCursor
			err 		error
		)
		filter := bson.M{"name": "Misty"}
		findOpts := options.Find()
		findOpts.SetSort(bson.D{{"age", -1}})
		cur,err = collection.Find(context.TODO(), filter, findOpts)
		if err!=nil{
			log.Fatal(err)
		}
		var moreDocs bool
		moreDocs = cur.Next(context.TODO())
		fmt.Printf("More Docs: %v", moreDocs)
}

func ExampleMongoCursor_TryNext(){
		var(
			collection *integrations.MongoCollection
			cur 	   *integrations.MongoCursor
			err 		error
		)
		filter := bson.M{"name": "Misty"}
		findOpts := options.Find()
		findOpts.SetSort(bson.D{{"age", -1}})
		cur,err = collection.Find(context.TODO(), filter, findOpts)
		if err!=nil{
			log.Fatal(err)
		}
		var moreDocs bool
		moreDocs = cur.TryNext(context.TODO())
		fmt.Printf("More Docs: %v", moreDocs)
}

func ExampleMongoCursor_Close(){
		var(
			collection *integrations.MongoCollection
			cur 	   *integrations.MongoCursor
			err 		error
		)
		filter := bson.M{"name": "Misty"}
		findOpts := options.Find()
		findOpts.SetSort(bson.D{{"age", -1}})
		cur,err = collection.Find(context.TODO(), filter, findOpts)
		if err!=nil{
			log.Fatal(err)
		}
		cur.Close(context.TODO())
}

func ExampleMongoCursor_All(){
		var(
			collection *integrations.MongoCollection
			cur 	   *integrations.MongoCursor
			err 		error
		)
		filter := bson.M{"name": "Misty"}
		findOpts := options.Find()
		findOpts.SetSort(bson.D{{"age", -1}})
		cur,err = collection.Find(context.TODO(), filter, findOpts)
		if err!=nil{
			log.Fatal(err)
		}
		var results []bson.D
		cur.All(context.TODO(), &results)
}

func ExampleMongoCursor_Decode(){
		var(
			collection *integrations.MongoCollection
			cur 	   *integrations.MongoCursor
			err 		error
		)
		filter := bson.M{"name": "Misty"}
		findOpts := options.Find()
		findOpts.SetSort(bson.D{{"age", -1}})
		cur,err = collection.Find(context.TODO(), filter, findOpts)
		if err!=nil{
			log.Fatal(err)
		}
		var results []bson.D
		for cur.Next(context.TODO()) {
			var elem bson.D
			err := cur.Decode(&elem)
			if err != nil {
				log.Fatal(err)
			}
			results = append(results, elem)
		}
}

func ExampleMongoCollection_InsertOne(){
		var(
			collection *integrations.MongoCollection
			err       error
			insertOneResult *mongo.InsertOneResult
		)
		ash := bson.D{{"name", "Alice"}}
		insertOneOpts := options.InsertOne()
		insertOneOpts.SetBypassDocumentValidation(false)
		insertOneResult,err = collection.InsertOne(context.TODO(), ash, insertOneOpts)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("inserted document with ID %v\n", insertOneResult.InsertedID)
}

func ExampleMongoCollection_FindOne(){
		var(
			sr 		   *integrations.MongoSingleResult
			collection *integrations.MongoCollection
			err  		error
		)
		filter := bson.M{"name": "Ash"}
		var resulto bson.D
		findOneOpts := options.FindOne()
		findOneOpts.SetComment("this is cool stuff")
		sr = collection.FindOne(context.TODO(), filter, findOneOpts)
		err = sr.Err()
		if err != nil {
			log.Fatal(err)
		}else{
			sr.Decode(&resulto)
		}
}

func ExampleMongoCollection_InsertMany(){
		var(
			insertManyResult   *mongo.InsertManyResult
			collection 		   *integrations.MongoCollection
			err  		        error
		)
		docs := []interface{}{
			bson.D{{"name", "Alice"}},
			bson.D{{"name", "Bob"}},
		}
		insertManyOpts := options.InsertMany()
		insertManyOpts.SetOrdered(true)
		insertManyResult,err = collection.InsertMany(context.TODO(), docs, insertManyOpts)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("inserted documents with IDs %v\n", insertManyResult.InsertedIDs...)
}

func ExampleMongoCollection_Find(){
		var(
			collection *integrations.MongoCollection
			cur 	   *integrations.MongoCursor
			err 		error
		)
		filter := bson.M{"name": "Misty"}
		findOpts := options.Find()
		findOpts.SetSort(bson.D{{"age", -1}})
		cur,err = collection.Find(context.TODO(), filter, findOpts)
		if err!=nil{
			log.Fatal(err)
		}
		var results []bson.D
		for cur.Next(context.TODO()) {
			var elem bson.D
			err := cur.Decode(&elem)
			if err != nil {
				log.Fatal(err)
			}
			results = append(results, elem)
		}
}

func ExampleMongoCollection_UpdateOne(){
		var(
			result     *mongo.UpdateResult
			collection *integrations.MongoCollection
			err 		error
		)
		filter := bson.M{"name": "Brock"}
		updateOpts := options.Update()
		updateOpts.SetBypassDocumentValidation(false)
		update := bson.D{{"$set", bson.D{{"name", "Brock"}, {"age", 22}, {"city", "Pallet Town"}}}}
		result,err = collection.UpdateOne(context.TODO(), filter, update, updateOpts)
		if err != nil {
			log.Fatal(err)
		}
		if result.MatchedCount != 0 {
			fmt.Println("matched and replaced an existing document")
			return
		}
		if result.UpsertedCount != 0 {
			fmt.Printf("inserted a new document with ID %v\n", result.UpsertedID)
		}
}

func ExampleMongoCollection_UpdateMany(){
		var(
			result     *mongo.UpdateResult
			collection *integrations.MongoCollection
			err 		error
		)
		filter := bson.M{"name": "Brock"}
		updateOpts := options.Update()
		updateOpts.SetBypassDocumentValidation(false)
		update := bson.D{{"$set", bson.D{{"name", "Brock"}, {"age", 22}, {"city", "Pallet Town"}}}}
		result,err = collection.UpdateMany(context.TODO(), filter, update, updateOpts)
		if err != nil {
			log.Fatal(err)
		}
		if result.MatchedCount != 0 {
			fmt.Println("matched and replaced an existing document")
			return
		}
}

func ExampleMongoCollection_DeleteOne(){
		var(
			result     *mongo.DeleteResult
			collection *integrations.MongoCollection
			err 		error
		)
		filter := bson.M{"name": "Brock"}
		deleteOpts := options.Delete()
		deleteOpts.SetHint("Go to cartoon network")
		result,err = collection.DeleteOne(context.TODO(), filter, deleteOpts)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("deleted %v document\n", result.DeletedCount)
}

func ExampleMongoCollection_DeleteMany(){
		var(
			result     *mongo.DeleteResult
			collection *integrations.MongoCollection
			err 		error
		)
		filter := bson.M{"name": "Brock"}
		deleteOpts := options.Delete()
		deleteOpts.SetHint("Go to cartoon network")
		result,err = collection.DeleteMany(context.TODO(), filter, deleteOpts)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("deleted %v documents\n", result.DeletedCount)
}

func ExampleWebGoV4(){
	app := keploy.NewApp("My-App", "81f83aeedddg7877685rfgui", "https://api.keploy.io", "0.0.0.0", "8080")
	router := webgo.NewRouter(&webgo.Config{
		Host:         "",
		Port:         "8080",
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
	}, []*webgo.Route{})	
	integrations.WebGoV4(app, router)
	router.Start()
}

func ExampleEchoV4(){
	e := echo.New()
	app := keploy.NewApp("Echo-App", "81f83aeehdjbh34hbfjrudf45646c65", "https://api.keploy.io",  "0.0.0.0", "6060")
	integrations.EchoV4(app, e)
	e.Start(":6060")
}

func ExampleWithClientUnaryInterceptor(){
	app := keploy.NewApp("CheckNoisyBody", "81f83aeeedddf453966347dc136c65", "", "0.0.0.0", "8080")
	conn, err := grpc.Dial("localhost:50051", grpc.WithInsecure(), integrations.WithClientUnaryInterceptor(app))
	if err != nil {
		log.Fatalf("Did not connect : %v", err)
	}
	defer conn.Close()
}