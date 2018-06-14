# broadcastor
Scalable message broadcaster

## Usage
```
# Run redis, API and 2 spreaders
> docker-compose -d

# Run the client interactive
> docker run -it broadcastor_client
```

## TODO
- [ ] Set expiration time for user (set with ping time from client to keep alive)
- [ ] Set expiration time for messages
- [ ] Remove/Delete rooms (and associated users)
- [ ] Use redis cluster
- [ ] Add multiple fields for user/message/room (name, description, image, etc.)
