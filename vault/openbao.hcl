ui = true
api_addr = "http://127.0.0.1:8200"

listener "tcp" {
  address     = "0.0.0.0:8200"
  tls_disable = 1
}

storage "postgresql" {
  connection_url = "postgres://user:password@db:5432/openbao?sslmode=disable"
  table          = "openbao_kv_store"
  ha_enabled     = "true"
  ha_table       = "openbao_ha_locks"
}
