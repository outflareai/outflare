# frozen_string_literal: true

require "json"
require "net/http"
require "securerandom"
require "time"
require "uri"
require_relative "outflare/version"

# Sending product events to Outflare from a Ruby server.
#
#   tracker = Outflare::Tracker.new(brand: "northpeak") # reads OUTFLARE_TOKEN and OUTFLARE_URL
#   tracker.identify(user_id: "u_123", traits: { plan: "pro" })
#   tracker.track(user_id: "u_123", event: "Order Completed", properties: { total: 42 })
#   tracker.close # before the process exits
#
# Calls are queued in memory and sent in batches to
# POST /v1/brands/{brand}/events by a background thread, with retries.
module Outflare
  class Error < StandardError; end

  # A queue of product events for one brand. Make one and share it.
  class Tracker
    MAX_BATCH = 500

    # token is an API token with customers.manage (default OUTFLARE_TOKEN);
    # keep it on your servers. on_error is called with the error and how
    # many events were lost when a batch is given up on.
    def initialize(brand:, token: ENV["OUTFLARE_TOKEN"], base_url: ENV["OUTFLARE_URL"], flush_at: 20, flush_interval: 10,
                   max_queue: 10_000, max_retries: 3, on_error: nil, transport: nil, sleeper: nil)
      raise ArgumentError, "Outflare::Tracker needs a token: pass token: or set OUTFLARE_TOKEN" if token.nil? || token.empty?
      raise ArgumentError, "Outflare::Tracker needs a brand: its id or slug" if brand.nil? || brand.empty?

      base = (base_url.nil? || base_url.empty? ? "https://app.outflare.ai" : base_url).sub(%r{/+\z}, "")
      @uri = URI("#{base}/v1/brands/#{URI.encode_www_form_component(brand)}/events")
      @token = token
      @flush_at = flush_at.clamp(1, MAX_BATCH)
      @flush_interval = flush_interval
      @max_queue = max_queue
      @max_retries = max_retries
      @on_error = on_error
      @transport = transport || method(:post)
      @sleeper = sleeper || ->(seconds) { sleep(seconds) }
      @queue = []
      @lock = Mutex.new
      @send_lock = Mutex.new
      @wake = ConditionVariable.new
      @closed = false
      @thread = Thread.new { run }
      at_exit { close }
    end

    # Something a person did.
    def track(event:, user_id: nil, properties: {}, **rest)
      raise ArgumentError, "track needs an event name" if event.nil? || event.to_s.empty?

      add("track", user_id, { event: event.to_s, properties: properties }, rest)
    end

    # Who a person is.
    def identify(user_id:, traits: {}, **rest)
      raise ArgumentError, "identify needs a user_id" if user_id.nil? || user_id.to_s.empty?

      add("identify", user_id, { traits: traits }, rest)
    end

    # The company a person belongs to.
    def group(group_id:, user_id: nil, traits: {}, **rest)
      raise ArgumentError, "group needs a group_id" if group_id.nil? || group_id.to_s.empty?

      add("group", user_id, { groupId: group_id.to_s, traits: traits }, rest)
    end

    # A page someone saw.
    def page(user_id: nil, name: nil, properties: {}, **rest)
      add("page", user_id, { name: name, properties: properties }, rest)
    end

    # A screen someone saw in an app.
    def screen(user_id: nil, name: nil, properties: {}, **rest)
      add("screen", user_id, { name: name, properties: properties }, rest)
    end

    # An id a person had before: joins previous_id to user_id.
    def alias(user_id:, previous_id:, **rest)
      raise ArgumentError, "alias needs a user_id and a previous_id" if user_id.to_s.empty? || previous_id.to_s.empty?

      add("alias", user_id, { previousId: previous_id.to_s }, rest)
    end

    # Send everything waiting now. Returns once it was sent or given up on.
    def flush
      @send_lock.synchronize do
        loop do
          batch = @lock.synchronize { @queue.shift(MAX_BATCH) }
          break if batch.empty?

          begin
            deliver(batch)
          rescue StandardError => e
            @on_error&.call(e, batch.length)
          end
        end
      end
      nil
    end

    # Send what waits and take no more. Call it before the process exits.
    def close
      return if @closed

      @lock.synchronize do
        @closed = true
        @wake.signal
      end
      @thread.join(1) unless Thread.current == @thread
      flush
    end

    private

    def add(type, user_id, fields, rest)
      anonymous_id = rest.delete(:anonymous_id)
      timestamp = rest.delete(:timestamp) || Time.now
      message_id = rest.delete(:message_id) || SecureRandom.uuid
      context = (rest.delete(:context) || {}).dup
      raise ArgumentError, "unexpected arguments: #{rest.keys.join(", ")}" unless rest.empty?
      raise ArgumentError, "#{type} needs a user_id or an anonymous_id" if user_id.to_s.empty? && anonymous_id.to_s.empty?

      timestamp = timestamp.utc.iso8601(3) if timestamp.respond_to?(:utc)
      context[:library] = { name: "outflare-ruby", version: VERSION }
      message = { type: type, messageId: message_id, timestamp: timestamp, context: context }.merge(fields)
      message[:userId] = user_id.to_s unless user_id.to_s.empty?
      message[:anonymousId] = anonymous_id.to_s unless anonymous_id.to_s.empty?
      lost = 0
      @lock.synchronize do
        raise Error, "the tracker is closed" if @closed

        @queue << message
        if @queue.length > @max_queue
          lost = @queue.length - @max_queue
          @queue.shift(lost)
        end
        @wake.signal if @queue.length >= @flush_at
      end
      @on_error&.call(Error.new("the queue is full, so the oldest events were dropped"), lost) if lost.positive?
      message_id
    end

    def run
      loop do
        @lock.synchronize do
          @wake.wait(@lock, @flush_interval) unless @closed
          return if @closed
        end
        flush
      end
    end

    def deliver(batch)
      body = JSON.generate(source: "ruby", batch: batch)
      attempt = 0
      loop do
        status = 0
        retry_after = 0
        begin
          status, retry_after = @transport.call(@uri, @token, body)
          return if status.between?(200, 299)
        rescue StandardError => e
          raise Error, "the server could not be reached: #{e.class}" if attempt >= @max_retries
        end
        # A refusal (a bad token, no such brand) won't change on a retry.
        raise Error, "the server refused the batch (#{status})" if status.between?(400, 499) && ![408, 429].include?(status)
        raise Error, "the server could not take the batch (#{status})" if attempt >= @max_retries

        @sleeper.call([retry_after.to_f.positive? ? retry_after.to_f : 0.5 * (2**attempt), 30].min)
        attempt += 1
      end
    end

    def post(uri, token, body)
      http = Net::HTTP.new(uri.host, uri.port)
      http.use_ssl = uri.scheme == "https"
      http.open_timeout = 5
      http.read_timeout = 10
      request = Net::HTTP::Post.new(uri.request_uri, "Content-Type" => "application/json", "Authorization" => "Bearer #{token}",
                                                     "User-Agent" => "outflare-ruby/#{VERSION}")
      request.body = body
      response = http.request(request)
      [response.code.to_i, response["Retry-After"].to_i]
    end
  end
end
