# Telegram Bot Adapter Specification

## Purpose

Telegram bot adapter that receives text messages from Telegram users via polling and ingests them into Knowledge Core through the existing `IngestTextService`.

## Requirements

### Requirement: Message Ingestion

The system SHALL receive text messages from Telegram private chats and ingest them into Knowledge Core via `IngestTextService.Ingest()`.

#### Scenario: Text message ingested successfully

- GIVEN a Telegram private chat is active and the bot is running
- WHEN a user sends a plain text message
- THEN the adapter extracts text, chat ID, user ID, and message ID from the update
- AND builds an `IngestTextRequest` with `source.type="telegram"` and `metadata.chat_id`
- AND calls `IngestTextService.Ingest()` with the request
- AND responds to the user with "Saved"

#### Scenario: Duplicate message detected

- GIVEN a text message has already been ingested with the same `message_id`
- WHEN the adapter receives the same message again
- THEN the adapter responds with "Duplicate"
- AND does NOT call `IngestTextService.Ingest()`

### Requirement: Command Handling

The system SHALL handle `/start` and `/help` commands with appropriate responses.

#### Scenario: /start command

- GIVEN the bot is running in a private chat
- WHEN a user sends `/start`
- THEN the adapter responds with a welcome message

#### Scenario: /help command

- GIVEN the bot is running in a private chat
- WHEN a user sends `/help`
- THEN the adapter responds with usage instructions explaining that plain text messages are ingested into Knowledge Core

### Requirement: Idempotency

The system SHALL use the Telegram `message_id` as the idempotency key when calling `IngestTextService.Ingest()`.

#### Scenario: Same message_id on repeated delivery

- GIVEN a message with `message_id=123` has been ingested
- WHEN the adapter receives another update containing `message_id=123`
- THEN the adapter does NOT duplicate the ingestion
- AND responds with "Duplicate"

### Requirement: Configuration

The system SHALL read the bot token from the `PROJECT_BRAIN_TELEGRAM_BOT_TOKEN` environment variable and operate in polling mode.

#### Scenario: Missing bot token

- GIVEN the `PROJECT_BRAIN_TELEGRAM_BOT_TOKEN` environment variable is not set
- WHEN the application starts
- THEN the adapter logs an error and does not start the polling loop
- AND the application continues running the HTTP server

#### Scenario: Bot token configured

- GIVEN the `PROJECT_BRAIN_TELEGRAM_BOT_TOKEN` environment variable is set
- WHEN the application starts
- THEN the adapter starts polling Telegram for updates using the configured token

### Requirement: Graceful Shutdown

The system SHALL handle in-flight Telegram updates gracefully when receiving SIGTERM.

#### Scenario: SIGTERM during active polling

- GIVEN the bot is actively polling for updates
- WHEN the application receives SIGTERM
- THEN the adapter stops polling and waits for any in-flight message processing to complete
- AND the HTTP server shuts down following its existing pattern

### Requirement: Error Handling

The system SHALL log errors from message processing and service calls without crashing the bot or the application.

#### Scenario: IngestTextService returns an error

- GIVEN the adapter receives a valid text message
- WHEN `IngestTextService.Ingest()` returns an error
- THEN the adapter logs the error with context (chat_id, message_id)
- AND responds to the user with a generic error message
- AND the bot continues processing subsequent updates

#### Scenario: Malformed Telegram update

- GIVEN the adapter receives a Telegram update that cannot be parsed
- WHEN processing the update fails
- THEN the adapter logs the error and discards the update
- AND the bot continues processing subsequent updates
