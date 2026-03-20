/**
 * Slinkd Node.js client.
 *
 * Usage:
 *   const { Slinkd } = require('./slinkd');
 *
 *   const ss = new Slinkd(); // reads SLINKD_API_KEY and SLINKD_HOST from env
 *   ss.alert('Orderbook empty: depth=0', { author: 'trading-bot' });
 *   ss.send('deploys', { type: 'deployment', text: 'v2 shipped', author: 'ci' });
 *
 * Zero dependencies — uses built-in fetch (Node 18+).
 * Fire-and-forget by default — errors are logged, never thrown.
 */

class Slinkd {
  /**
   * @param {Object} opts
   * @param {string} [opts.host]           - Server URL (default: SLINKD_HOST env or http://localhost:8080)
   * @param {string} [opts.apiKey]         - API key (default: SLINKD_API_KEY env)
   * @param {string} [opts.defaultChannel] - Default channel for alert/signal (default: 'alerts')
   * @param {string} [opts.author]         - Default author name
   */
  constructor({ host, apiKey, defaultChannel = 'alerts', author = '' } = {}) {
    this.host = (host || process.env.SLINKD_HOST || 'http://localhost:8080').replace(/\/+$/, '');
    this.apiKey = apiKey || process.env.SLINKD_API_KEY || '';
    this.defaultChannel = defaultChannel;
    this.author = author;
    this._cooldowns = new Map(); // text -> timestamp
  }

  /**
   * Send an event to a channel.
   * @param {string} channel - Channel ID
   * @param {Object} opts
   * @param {string} [opts.type='signal'] - Event type (message, alert, signal, deployment, breaking_change)
   * @param {string} opts.text            - Event text
   * @param {string} [opts.author]        - Author override
   * @param {Object} [opts.data]          - Arbitrary JSON payload
   * @returns {Promise<Object|null>}      - Response body, or null on error
   */
  async send(channel, { type = 'signal', text, author, data } = {}) {
    try {
      const body = { type, text, author: author || this.author };
      if (data) body.data = data;

      const res = await fetch(`${this.host}/channels/${channel}/events`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${this.apiKey}`,
        },
        body: JSON.stringify(body),
        signal: AbortSignal.timeout(5000),
      });

      if (!res.ok) {
        console.error(`[slinkd] POST #${channel} returned ${res.status}`);
        return null;
      }

      return await res.json();
    } catch (err) {
      console.error(`[slinkd] Failed to send to #${channel}:`, err.message);
      return null;
    }
  }

  /**
   * Send a rate-limited alert. Suppresses duplicate text within the cooldown window.
   * @param {string} text               - Alert message
   * @param {Object} [opts]
   * @param {string} [opts.author]      - Author override
   * @param {Object} [opts.data]        - Arbitrary JSON payload
   * @param {string} [opts.channel]     - Channel override (default: this.defaultChannel)
   * @param {number} [opts.cooldown=60] - Seconds to suppress duplicate text
   * @returns {Promise<Object|null>}    - Response body, null if suppressed or on error
   */
  async alert(text, { author, data, channel, cooldown = 60 } = {}) {
    const now = Date.now();
    const lastSent = this._cooldowns.get(text) || 0;
    if (now - lastSent < cooldown * 1000) return null;
    this._cooldowns.set(text, now);

    return this.send(channel || this.defaultChannel, { type: 'alert', text, author, data });
  }

  /**
   * Send a rate-limited signal (lower severity, longer default cooldown).
   * @param {string} text                 - Signal message
   * @param {Object} [opts]
   * @param {string} [opts.author]        - Author override
   * @param {Object} [opts.data]          - Arbitrary JSON payload
   * @param {string} [opts.channel]       - Channel override
   * @param {number} [opts.cooldown=300]  - Seconds to suppress duplicate text
   * @returns {Promise<Object|null>}
   */
  async signal(text, { author, data, channel, cooldown = 300 } = {}) {
    const now = Date.now();
    const lastSent = this._cooldowns.get(text) || 0;
    if (now - lastSent < cooldown * 1000) return null;
    this._cooldowns.set(text, now);

    return this.send(channel || this.defaultChannel, { type: 'signal', text, author, data });
  }
}

module.exports = { Slinkd };
