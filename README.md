# broadcastor
Scalable message broadcaster

## Usage
local
```
> docker-compose up -d redis
# in configuration files, all references to `redis:6379` must be change to `localhost:6379`
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
# first frame may be not rendered correctly, just press space to render correctly
```

Client commands
```
/rooms -> list rooms
/newroom -> create a new room
/connect ROOM_ID -> connect to a room ROOM_ID as a new user, or last room created
```

e.g:
```
/newroom
01CFZFPPF7PGQK1RSJEE15GYQD
/connect
connected with user ID: 01CFZFPRCPDB09G283JRV3CRFJ
test
Thu Jun 14 16:02:56 UTC 2018 | test
```
## TODO
- [ ] Set expiration time for user (set with ping time from client to keep alive)
- [ ] Set expiration time for messages
- [ ] Remove/Delete rooms (and associated users)
- [ ] Use redis cluster
- [ ] Add multiple fields for user/message/room (name, description, image, etc.)
