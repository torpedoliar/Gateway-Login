package main

import (
	_ "github.com/go-chi/chi/v5"
	_ "github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/redis/go-redis/v9"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/robfig/cron/v3"
	_ "github.com/spf13/viper"
	_ "github.com/rs/zerolog"
	_ "github.com/google/uuid"
	_ "gopkg.in/yaml.v3"
	_ "github.com/AlecAivazis/survey/v2"
	_ "github.com/stretchr/testify"
	_ "github.com/testcontainers/testcontainers-go"
	_ "github.com/testcontainers/testcontainers-go/modules/postgres"
	_ "github.com/testcontainers/testcontainers-go/modules/mysql"
	_ "github.com/prometheus/client_golang/prometheus"
)

func main() {}
