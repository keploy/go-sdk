package integrations

import(
	"fmt"
	"context"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"log"
	"go.mongodb.org/mongo-driver/bson"
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
	collection = integrations.NewMongoCollection(client.Database("test").Collection("client"))	
}

func ExampleMongoSingleResult_Err(){
	func (w http.ResponseWriter, r *http.Request){
		var(
			sr 		   *intergrations.MongoSingleResult
			collection *integrations.MongoCollection
		)
		collection = integrations.NewMongoCollection(client.Database("test").Collection("Trainer"))
		filter := bson.M{"name": "Ash"}
		findOneOpts := options.FindOne()
		findOneOpts.SetComment("this is cool stuff")
		sr = collection.FindOne(r.Context(), filter, findOneOpts)
		err := sr.Err()
	}
}

func ExampleMongoSingleResult_Decode(){
	func (w http.ResponseWriter, r *http.Request){
		var(
			sr 		   *intergrations.MongoSingleResult
			collection *integrations.MongoCollection
		)
		collection = integrations.NewMongoCollection(client.Database("test").Collection("Trainer"))
		filter := bson.M{"name": "Ash"}
		var result Trainer
		findOneOpts := options.FindOne()
		findOneOpts.SetComment("this is cool stuff")
		sr = collection.FindOne(r.Context(), filter, findOneOpts)
		err := sr.Decode(&result)
	}
}

func ExampleMongoCursor_Err(){
	func (w http.ResponseWriter, r *http.Request){
		var(
			collection *integrations.MongoCollection
			cur 	   *integrations.MongoCursor
			err 		error
		)
		collection = integrations.NewMongoCollection(client.Database("test").Collection("Trainer"))
		filter := bson.M{"name": "Misty"}
		findOpts := options.Find()
		findOpts.SetSort(bson.D{{"age", -1}})
		cur,_ = collection.Find(r.Context(), filter, findOpts)
		err = cur.Err()
	}
}

func ExampleMongoCursor_Next(){
	func (w http.ResponseWriter, r *http.Request){
		var(
			collection *integrations.MongoCollection
			cur 	   *integrations.MongoCursor
			err 		error
		)
		collection = integrations.NewMongoCollection(client.Database("test").Collection("Trainer"))
		filter := bson.M{"name": "Misty"}
		findOpts := options.Find()
		findOpts.SetSort(bson.D{{"age", -1}})
		cur,err = collection.Find(r.Context(), filter, findOpts)
		if err!=nil{
			log.fatal(err)
		}
		var moreDocs bool
		moreDocs = cur.Next(r.Context())
	}
}

func ExampleMongoCursor_TryNext(){
	func (w http.ResponseWriter, r *http.Request){
		var(
			collection *integrations.MongoCollection
			cur 	   *integrations.MongoCursor
			err 		error
		)
		collection = integrations.NewMongoCollection(client.Database("test").Collection("Trainer"))
		filter := bson.M{"name": "Misty"}
		findOpts := options.Find()
		findOpts.SetSort(bson.D{{"age", -1}})
		cur,err = collection.Find(r.Context(), filter, findOpts)
		if err!=nil{
			log.fatal(err)
		}
		var moreDocs bool
		moreDocs = cur.TryNext(r.Context())
	}
}

func ExampleMongoCursor_Close(){
	func (w http.ResponseWriter, r *http.Request){
		var(
			collection *integrations.MongoCollection
			cur 	   *integrations.MongoCursor
			err 		error
		)
		collection = integrations.NewMongoCollection(client.Database("test").Collection("Trainer"))
		filter := bson.M{"name": "Misty"}
		findOpts := options.Find()
		findOpts.SetSort(bson.D{{"age", -1}})
		cur,err = collection.Find(r.Context(), filter, findOpts)
		if err!=nil{
			log.fatal(err)
		}
		cur.Close(r.Context())
	}
}

func ExampleMongoCursor_All(){
	func (w http.ResponseWriter, r *http.Request){
		var(
			collection *integrations.MongoCollection
			cur 	   *integrations.MongoCursor
			err 		error
		)
		collection = integrations.NewMongoCollection(client.Database("test").Collection("Trainer"))
		filter := bson.M{"name": "Misty"}
		findOpts := options.Find()
		findOpts.SetSort(bson.D{{"age", -1}})
		cur,err = collection.Find(r.Context(), filter, findOpts)
		if err!=nil{
			log.fatal(err)
		}
		var results []Trainer
		cur.All(r.Context(), &results)
	}
}

func ExampleMongoCursor_Decode(){
	func (w http.ResponseWriter, r *http.Request){
		var(
			collection *integrations.MongoCollection
			cur 	   *integrations.MongoCursor
			err 		error
		)
		collection = integrations.NewMongoCollection(client.Database("test").Collection("Trainer"))
		filter := bson.M{"name": "Misty"}
		findOpts := options.Find()
		findOpts.SetSort(bson.D{{"age", -1}})
		cur,err = collection.Find(r.Context(), filter, findOpts)
		if err!=nil{
			log.fatal(err)
		}
		var results []Trainer
		for cur.Next(r.Context()) {
			var elem Trainer
			err := cur.Decode(&elem)
			if err != nil {
				logger.Error(err.Error())
			}
			results = append(results, elem)
		}
	}
}

func ExampleMongoCollection_InsertOne(){
	func (w http.ResponseWriter, r *http.Request){
		var(
			collection *integrations.MongoCollection
			err       error
			insertOneResult *mongo.InsertOneResult
		)
		collection = integrations.NewMongoCollection(client.Database("test").Collection("Trainer"))
		ash := Trainer{"Ash", 10, "Pallet Town"}
		insertOneOpts := options.InsertOne()
		insertOneOpts.SetBypassDocumentValidation(false)
		insertOneResult,err = collection.InsertOne(r.Context(), ash, insertOneOpts)
		if err != nil {
			log.Fatal(err)
		}
	}
}

func ExampleMongoCollection_FindOne(){
	func (w http.ResponseWriter, r *http.Request){
		var(
			sr 		   *intergrations.MongoSingleResult
			collection *integrations.MongoCollection
			err  		error
		)
		collection = integrations.NewMongoCollection(client.Database("test").Collection("Trainer"))
		filter := bson.M{"name": "Ash"}
		var resulto Trainer
		findOneOpts := options.FindOne()
		findOneOpts.SetComment("this is cool stuff")
		sr = collection.FindOne(r.Context(), filter, findOneOpts)
		err = sr.Err()
		if err != nil {
			log.Fatal(err)
		}else{
			sr.Decode(&resulto)
		}
	}
}

func ExampleMongoCollection_InsertMany(){
	func (w http.ResponseWriter, r *http.Request){
		var(
			insertManyResult   *mongo.InsertManyResult
			collection 		   *integrations.MongoCollection
			err  		        error
		)
		collection = integrations.NewMongoCollection(client.Database("test").Collection("Trainer"))
		ash := Trainer{"Ash", 10, "Pallet Town"}
		misty := Trainer{"Misty", 10, "Cerulean City"}
		brock := Trainer{"Brock", 15, "Pewter City"}
		trainers := []interface{}{ash, misty, brock}
		insertManyOpts := options.InsertMany()
		insertManyOpts.SetOrdered(true)
		insertManyResult,err = collection.InsertMany(r.Context(), trainers, insertManyOpts)
		if err != nil {
			log.Fatal(err)
		}
	}
}

func ExampleMongoCollection_Find(){
	func (w http.ResponseWriter, r *http.Request){
		var(
			collection *integrations.MongoCollection
			cur 	   *integrations.MongoCursor
			err 		error
		)
		collection = integrations.NewMongoCollection(client.Database("test").Collection("Trainer"))
		filter := bson.M{"name": "Misty"}
		findOpts := options.Find()
		findOpts.SetSort(bson.D{{"age", -1}})
		cur,err = collection.Find(r.Context(), filter, findOpts)
		if err!=nil{
			log.Fatal(err)
		}
		var results []Trainer
		for cur.Next(r.Context()) {
			var elem Trainer
			err := cur.Decode(&elem)
			if err != nil {
				log.Fatal(err)
			}
			results = append(results, elem)
		}
	}
}

func ExampleMongoCollection_UpdateOne(){
	func (w http.ResponseWriter, r *http.Request){
		var(
			result     *mongo.UpdateResult
			collection *integrations.MongoCollection
			err 		error
		)
		collection = integrations.NewMongoCollection(client.Database("test").Collection("Trainer"))
		filter := bson.M{"name": "Brock"}
		updateOpts := options.Update()
		updateOpts.SetBypassDocumentValidation(false)
		update := bson.D{{"$set", bson.D{{"name", "Brock"}, {"age", 22}, {"city", "Pallet Town"}}}}
		result,err = collection.UpdateOne(r.Context(), filter, update, updateOpts)
		if err != nil {
			log.Fatal(err)
		}
	}
}

func ExampleMongoCollection_UpdateMany(){
	func (w http.ResponseWriter, r *http.Request){
		var(
			result     *mongo.UpdateResult
			collection *integrations.MongoCollection
			err 		error
		)
		collection = integrations.NewMongoCollection(client.Database("test").Collection("Trainer"))
		filter := bson.M{"name": "Brock"}
		updateOpts := options.Update()
		updateOpts.SetBypassDocumentValidation(false)
		update := bson.D{{"$set", bson.D{{"name", "Brock"}, {"age", 22}, {"city", "Pallet Town"}}}}
		result,err = collection.UpdateMany(r.Context(), filter, update, updateOpts)
		if err != nil {
			log.Fatal(err)
		}
	}
}

func ExampleMongoCollection_DeleteOne(){
	func (w http.ResponseWriter, r *http.Request){
		var(
			result     *mongo.DeleteResult
			collection *integrations.MongoCollection
			err 		error
		)
		collection = integrations.NewMongoCollection(client.Database("test").Collection("Trainer"))
		filter := bson.M{"name": "Brock"}
		deleteOpts := options.Delete()
		deleteOpts.SetHint("Go to cartoon network")
		result,err = collection.DeleteOne(r.Context(), filter, deleteOpts)
		if err != nil {
			log.Fatal(err)
		}
	}
}

func ExampleMongoCollection_DeleteMany(){
	func (w http.ResponseWriter, r *http.Request){
		var(
			result     *mongo.DeleteResult
			collection *integrations.MongoCollection
			err 		error
		)
		collection = integrations.NewMongoCollection(client.Database("test").Collection("Trainer"))
		filter := bson.M{"name": "Brock"}
		deleteOpts := options.Delete()
		deleteOpts.SetHint("Go to cartoon network")
		result,err = collection.DeleteMany(r.Context(), filter, deleteOpts)
		if err != nil {
			log.Fatal(err)
		}
	}
}

func ExampleWebGoV4(){
	kApp := keploy.NewApp("My-App", "81f83aeedddg7877685rfgui", "https://api.keploy.io", "0.0.0.0", "8080")
	router := webgo.NewRouter(&webgo.Config{
		Host:         "",
		Port:         "8080",
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
	}, routes())	
	integrations.WebGoV4(kApp, router)
	router.Start()
}

func ExampleWebGoV6(){
	kApp := keploy.NewApp("My-App", "81f83aeedddg7877685rfgui", "https://api.keploy.io", "0.0.0.0", "8080")
	router := webgo.NewRouter(&webgo.Config{
		Host:         "",
		Port:         "8080",
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
	}, routes())	
	integrations.WebGoV6(kApp, router)
	router.Start()
}

func ExampleEchoV4(){
	e := echo.New()
	kApp := keploy.NewApp("Echo-App", "81f83aeehdjbh34hbfjrudf45646c65", "https://api.keploy.io",  "0.0.0.0", "6060")
	integrations.EchoV4(kApp, e)
	e.Start(":6060")
}
