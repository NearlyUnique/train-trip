This we app is designed to give persistent (for a human journey) information about the timeliness and platforms for specific routes using https://realtimetrains.github.io/api-specification/ as a data source.

## API Reference

- Full spec: https://realtimetrains.github.io/api-specification/specification/main.yml
The web version https://www.realtimetrains.co.uk/ already existing requires selecting the correct train and, for routes with changes, independently opening a new page with that section of the route.

The UI for this should show the current time focused easy to read information with options to check other sections of a multi train journey, including where the next train is currently and when it will arrive, changes or delays should be obvious. In the event of a delayed train highlight train change times and in the event of missing a train show when the next available train will arrive.

the backend for this app will be written in "simple go", that is a flat file structure delivering htmx results. the secrets cannot be exposed publicly so must be on the server. the site will be running behind an nginx proxy on a raspberry pi arm6 device. minimal javascript should be used.

With go and javascript do not use any 3rd part package unless needed. e.g. the API spec for the parts of real-time-trains is small and can easily be coded in go using the standard library. whereas logging can use slog from the standard library, gorilla makes sense for an http server.

colours can be from https://uchu.style/ (https://code.webb.page/nevercease/uchu.git/about/documentation/FAQ.md)

## Rate Limits

- https://data.rtt.io
    - 30/min, 750/hour, 9000/day, 30000/week