# frozen_string_literal: true

require "minitest/autorun"
require "outflare"

class TrackerTest < Minitest::Test
  def tracker(answers = [], **opts)
    @calls = []
    transport = lambda do |uri, token, body|
      @calls << { url: uri.to_s, token: token, body: JSON.parse(body) }
      status = answers.shift || 202
      raise IOError, "network down" if status.zero?

      [status, 0]
    end
    Outflare::Tracker.new(brand: "northpeak", token: "oft_test_not_a_real_token", base_url: "https://outflare.example/",
                          flush_interval: 3600, transport: transport, sleeper: ->(_) {}, **opts)
  end

  def test_events_are_queued_batched_and_sent_with_the_token
    t = tracker
    id = t.track(user_id: "u1", event: "Order Completed", properties: { total: 42 })
    t.identify(user_id: "u1", traits: { plan: "pro" })
    t.group(user_id: "u1", group_id: "org_7")
    t.page(anonymous_id: "a1", name: "Pricing")
    t.screen(anonymous_id: "a1", name: "Home")
    t.alias(user_id: "u1", previous_id: "a1")
    assert_empty @calls
    t.flush
    assert_equal 1, @calls.length
    call = @calls.first
    assert_equal "https://outflare.example/v1/brands/northpeak/events", call[:url]
    assert_equal "oft_test_not_a_real_token", call[:token]
    assert_equal "ruby", call[:body]["source"]
    assert_equal %w[track identify group page screen alias], call[:body]["batch"].map { |m| m["type"] }
    first = call[:body]["batch"].first
    assert_equal id, first["messageId"]
    assert_equal "Order Completed", first["event"]
    assert_equal "outflare-ruby", first["context"]["library"]["name"]
    refute call[:body]["batch"][3].key?("userId")
    t.close
  end

  def test_close_sends_what_waits_and_takes_no_more
    t = tracker
    t.track(user_id: "u1", event: "A")
    t.close
    assert_equal 1, @calls.length
    assert_raises(Outflare::Error) { t.track(user_id: "u1", event: "B") }
  end

  def test_a_busy_server_is_retried_and_a_refusal_is_not
    lost = []
    t = tracker([503, 0, 202, 401], on_error: ->(e, n) { lost << [e.message, n] })
    t.track(user_id: "u1", event: "A")
    t.flush
    assert_equal 3, @calls.length
    assert_empty lost
    t.track(user_id: "u1", event: "B")
    t.flush
    assert_equal 4, @calls.length
    assert_equal [["the server refused the batch (401)", 1]], lost
    t.close
  end

  def test_calls_say_what_is_missing
    t = tracker
    assert_raises(ArgumentError) { t.track(event: "A") }
    assert_raises(ArgumentError) { t.track(user_id: "u1", event: "") }
    assert_raises(ArgumentError) { t.track(user_id: "u1", event: "A", colour: "red") }
    assert_raises(ArgumentError) { Outflare::Tracker.new(brand: "northpeak", token: "") }
    t.close
  end
end
