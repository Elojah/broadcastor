# [OPTION] [MESSAGE] Add new `SubscriberOption` and `MessageOptions`

Add new options and their implementations:
- `MessageOption`:
  - `WithErrorHandler`: Allows the user to specify a custom error handler for processing errors for a specific message.
- `SubscriberOption`:
  - `WithDefaultMessageOption`: Allows the user to set default message options for a subscriber, which will be applied to all messages processed by that subscriber.
