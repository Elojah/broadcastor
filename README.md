# broadcastor
Scalable message broadcaster

## Usage
local
```
> make dep
> make
> ./bin/bc_spreader -c bin config_spreader_0.json
> ./bin/bc_spreader -c bin config_spreader_1.json
> ./bin/bc_api -c bin/config_api.json
> ./bin/bc_client -c bin/config_client_0.json
```

docker-compose
```
# Run redis, API and 2 spreaders
> docker-compose up -d redis spreader0 spreader1 api

# Run clients interactive
> docker-compose run --rm client_0
> docker-compose run --rm client_1
> docker-compose run --rm client_2
```

Client commands
```
/rooms -> list rooms
/newroom -> create a new room
/connect ROOM_ID -> connect to a room ROOM_ID as a new user
```

## TODO
- [ ] Set expiration time for user (set with ping time from client to keep alive)
- [ ] Set expiration time for messages
- [ ] Remove/Delete rooms (and associated users)
- [ ] Use redis cluster
- [ ] Add multiple fields for user/message/room (name, description, image, etc.)
