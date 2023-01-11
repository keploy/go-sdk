package kserver

import(
	"go.keploy.io/server/server"

	"fmt"
	"os/exec"
	"strings"
	"time"
	"runtime"
)
const defaultVersion = "0.1.0-dev"
func startKeployServer(version string) { //Go routine for starting the server
	server.Server(version)
}

func StartAsync(){
	fmt.Println("Starting the keploy server")
	version, err := exec.Command("sh", "-c", "go list -m 'go.keploy.io/server'").Output() //Getting the OS version
	if err != nil{
		fmt.Println("Error getting the keploy version in", runtime.GOOS, "using the default version instead.") //If the above command does not work, use the default version instead.
		go startKeployServer(defaultVersion)
	}else{
		ver := strings.Split(string(version), " ")[1] //getting the version number from the library info.
		go startKeployServer(string(ver[1:]))
	}

	time.Sleep(1 * time.Second)

}