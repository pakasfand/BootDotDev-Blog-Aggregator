package config

import (
	"encoding/json"
	"fmt"
	"os"
)

const configFileName = ".gatorconfig.json"

type Config struct {
	DBUrl string `json:"db_url"`
	CurrentUserName string `json:"current_user_name"`
}

func Read() Config {
	homeDir, _ := os.UserHomeDir()
	configFilePath := fmt.Sprintf(homeDir + "/%s", configFileName)

	fileData, err := os.ReadFile(configFilePath)
	if err != nil {
		fmt.Println("Failed to read config file!")
	}

	var config Config
	_ = json.Unmarshal(fileData, &config)
	return config
}

func SetUser(userName string) error {
	homeDir, _ := os.UserHomeDir()
	configFilePath := fmt.Sprintf(homeDir + "/%s", configFileName)

	fileData, err := os.ReadFile(configFilePath)
	if err != nil {
		fmt.Println("Failed to read config file!")
		return err
	}

	var config Config
	json.Unmarshal(fileData, &config)
	config.CurrentUserName = userName
	fileData, _ = json.Marshal(config);

	err = os.WriteFile(configFilePath, fileData, 0o644)
	if err != nil {
		fmt.Println("Failed to write to the config file!")
		return err
	}
	return nil
}