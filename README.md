# Swarmind frontend
Adopted from LocalAI directly   
Frontend changes from LocalAI upstream can be synced and merged here    

# Launching
```
CONTRACT_ADDRESS="..." CONTRACT_ABI='...' LISTEN_URL='...' API_URL='...' COOKIE_DOMAIN='...' COOKIE_INSECURE='something is present here - cookie not secure' go run frontend_standalone/main.go
```
It runs at `:8081` port   

# Used files
- `core/http/views` html templates
- `core/http/static` hacked to work standalone (Capitalized some symbols for building)
- `frontend_standalone/main.go` as entry point 
