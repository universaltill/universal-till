package common

import "os"

type Config struct {
    ListenAddr    string
    DefaultLocale string
    Env           string
}

func ConfigFromEnv() Config {
    addr := os.Getenv("UT_LISTEN_ADDR")
    if addr == "" { addr = ":8080" }
    locale := os.Getenv("UT_DEFAULT_LOCALE")
    if locale == "" { locale = "en" }
    env := os.Getenv("UT_ENV")
    if env == "" { env = "dev" }
    return Config{ListenAddr: addr, DefaultLocale: locale, Env: env}
}
