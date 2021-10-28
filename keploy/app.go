package keploy

func NewApp(name, licenseKey string) *App {
	return &App{
		Name:       name,
		LicenseKey: licenseKey,
	}
}

type App struct {
	Name string
	LicenseKey string
}

func Test()  {
	// fetch test cases from web server and save to memory

	// call the service for each test case

	//
}
