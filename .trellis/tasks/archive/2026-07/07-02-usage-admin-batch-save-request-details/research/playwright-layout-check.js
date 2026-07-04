async page => {
  const results = [];
  const consoleErrors = [];
  page.on("console", msg => {
    if (msg.type() === "error") consoleErrors.push(msg.text());
  });
  page.on("pageerror", err => consoleErrors.push(err.message));

  await page.addInitScript(() => {
    const key = {
      id: "alice-key",
      name: "Alice",
      enabled: true,
      preview: "cpa_...live",
      rpm: 12,
      limits: { five_hour_usd: 2.5, daily_usd: 5, weekly_usd: 30, monthly_usd: 25 },
      reset_points: { "5h": 0, "24h": 0, "7d": 0, month: 0 },
      usage_windows: {
        "5h": { range: "5h", used_usd: 0.22, limit_usd: 2.5, used_percent: 0.088, reset_at_ms: null },
        "24h": { range: "24h", used_usd: 0.46, limit_usd: 5, used_percent: 0.092, reset_at_ms: null },
        "7d": { range: "7d", used_usd: 1.8, limit_usd: 30, used_percent: 0.06, reset_at_ms: null },
        month: { range: "month", used_usd: 6.2, limit_usd: 25, used_percent: 0.248, reset_at_ms: null },
      },
      pricing: {
        priced_model_count: 1,
        models: [{
          model: "gpt-5.5",
          input_per_million: 5,
          output_per_million: 30,
          cache_read_per_million: 0.5,
          cache_creation_per_million: 5,
        }],
      },
    };
    const event = {
      request_id: "req_visual_a",
      event_hash: "evt_visual_a",
      timestamp_ms: Date.now() - 10000,
      model: "gpt-5.5",
      requested_model: "gpt-5.5",
      endpoint: "/v1/responses",
      status: "success",
      failed: false,
      status_code: 200,
      latency_ms: 1280,
      ttft_ms: 320,
      input_tokens: 1000,
      output_tokens: 500,
      cached_tokens: 800,
      cache_read_tokens: 0,
      cache_creation_tokens: 0,
      reasoning_tokens: 320,
      total_tokens: 1500,
      cost: 0.016,
      cost_source: "key_policy",
      price_model: "gpt-5.5",
      service_tier: "priority",
      reasoning_effort: "high",
      api_key_preview: "94c1ab2d...51ee22",
      key: { id: "alice-key", name: "Alice", preview: "cpa_...live", enabled: true },
      cost_breakdown: {
        source: "key_policy",
        price_model: "gpt-5.5",
        unit: "usd_per_1m_tokens",
        service_tier: "priority",
        service_tier_multiplier: 2.5,
        prices: {
          input_per_million: 5,
          output_per_million: 30,
          cache_read_per_million: 0.5,
          cache_creation_per_million: 5,
        },
        tokens: {
          input: 1000,
          cached_input: 800,
          cpamp_cached_input: 800,
          billable_uncached_input: 200,
          cache_read: 0,
          cache_creation: 0,
          fine_grained_cache_read: 0,
          fine_grained_cache_creation: 0,
          effective_cache_read_for_hit_rate: 800,
          cache_hit_rate: 0.8,
          cache_semantics: "cpamp_compatible_cached_tokens",
          total_cache_activity: 800,
          output: 500,
          reasoning: 320,
          visible_output_estimate: 180,
          total: 1500,
        },
        costs: {
          input: 0.0025,
          cached_input: 0.001,
          cache_read: 0,
          cache_creation: 0,
          output: 0.0375,
          subtotal: 0.0164,
          total: 0.041,
        },
      },
      accounting: {
        selected_range: "24h",
        included_windows: ["5h", "24h", "7d", "month"],
        window_from_ms: Date.now() - 86400000,
        window_to_ms: Date.now(),
        reset_at_ms: null,
        current_window_limit_usd: 5,
        current_window_remaining_usd: 4.54,
      },
      quota: { used_percent: 8.2, plan: "pro" },
      failure_brief: "",
      failure: "",
    };
    const usage = {
      range: "24h",
      from_ms: Date.now() - 86400000,
      to_ms: Date.now(),
      quota: { limit_usd: 5, used_usd: 0.46, remaining_usd: 4.54, used_percent: 0.092 },
      summary: {
        total_calls: 8,
        success_calls: 8,
        failure_calls: 0,
        success_rate: 1,
        total_cost: 0.46,
        total_tokens: 18000,
        cached_tokens: 13000,
        output_tokens: 2600,
        reasoning_tokens: 1800,
        cost_source: "key_policy",
      },
      timeline: [],
      model_share: [{ model: "gpt-5.5", calls: 8, tokens: 18000, cost: 0.46 }],
      model_stats: [],
      api_key_stats: [],
    };
    const codexRequest = {
      request_id: "cc_visual_a",
      model: "gpt-5.5",
      path: "/v1/responses",
      started_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
      ended_at: new Date().toISOString(),
      duration_ms: 2100,
      status: "completed",
      protection: "protected_clean",
      folded: true,
      passthrough: false,
      passthrough_reason: null,
      key_identity: { known: true, source: "key_policy_state", id: "alice-key", name: "Alice", preview: "cpa_...live", enabled: true },
      rounds: [{ round: 1, reasoning_tokens: 320, n: null, decision: "clean", buffered: ["message"], truncation_match: false }],
      latest_round: 1,
      latest_reasoning_tokens: 320,
      first_truncation_round: null,
      first_truncation_reasoning_tokens: null,
      first_truncation_n: null,
      first_truncation_decision: null,
      continuation_count: 0,
      truncation_match: false,
      final_status: "completed",
      stopped_reason: "natural",
      failure_reason: null,
      failure_detail: null,
    };
    window.EventSource = class {
      constructor(url) {
        this.url = url;
        this.readyState = 1;
        this.listeners = {};
        setTimeout(() => {
          if (this.onopen) this.onopen({});
          this.dispatch("ready", { ok: true });
        }, 30);
      }
      addEventListener(name, fn) {
        this.listeners[name] = this.listeners[name] || [];
        this.listeners[name].push(fn);
      }
      dispatch(name, data) {
        for (const fn of this.listeners[name] || []) fn({ data: JSON.stringify(data) });
      }
      close() {
        this.readyState = 2;
      }
    };
    window.fetch = async input => {
      const url = String(input);
      const ok = data => new Response(JSON.stringify(data), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
      if (url.includes("/admin/api/keys/limits")) return ok({ ok: true, keys: [key] });
      if (url.includes("/admin/api/keys")) return ok({ keys: [key] });
      if (url.includes("/admin/api/events")) {
        return ok({
          key_id: url.includes("key_id=all") ? "all" : "alice-key",
          range: "24h",
          from_ms: Date.now() - 86400000,
          to_ms: Date.now(),
          reset_at_ms: null,
          quota: null,
          events: [event],
        });
      }
      if (url.includes("/api/me")) return ok({ me: key });
      if (url.includes("/api/usage")) return ok(usage);
      if (url.includes("/api/events")) return ok({
        range: "24h",
        from_ms: usage.from_ms,
        to_ms: usage.to_ms,
        reset_at_ms: null,
        quota: usage.quota,
        events: [event],
        has_more: false,
      });
      if (url.includes("status")) {
        return ok({
          ok: true,
          uptime_seconds: 120,
          counters: {
            total_requests: 8,
            active_requests: 0,
            folded_requests: 8,
            continuations: 1,
            truncation_hits: 1,
            failures: 0,
          },
          upstream: { ok: true },
          config: { upstream_host: "cpa:8317" },
          last_request_at: new Date().toISOString(),
          last_continuation_at: new Date().toISOString(),
          last_error_at: null,
        });
      }
      if (url.includes("requests")) return ok({ requests: [codexRequest], max_requests: 200 });
      if (url.includes("logs")) return ok({ events: [], max_events: 800 });
      return ok({});
    };
  });

  const pages = [
    {
      name: "usage-admin",
      url: "file:///D:/Dev/20_Software/23_Reference/llm-gateway/CodexCont/cpa_usage_portal/static/admin.html",
      must: ["保存全部", "全部 Key", "用户/Key", "Alice"],
      detail: ["Token 组成", "费用组成", "CPAMP 缓存命中"],
    },
    {
      name: "usage-dashboard",
      url: "file:///D:/Dev/20_Software/23_Reference/llm-gateway/CodexCont/cpa_usage_portal/static/dashboard.html",
      must: ["CPA 用量自助页", "模型分布", "最近请求", "Alice"],
      detail: ["Token 组成", "费用组成", "CPAMP 缓存命中"],
    },
    {
      name: "codexcont-dashboard",
      url: "file:///D:/Dev/20_Software/23_Reference/llm-gateway/CodexCont/middleware/dashboard.html",
      must: ["CodexCont 保护状态面板", "用户/Key", "Alice", "cc_visual_a"],
      detail: ["思维链保护判断", "身份来源"],
    },
  ];
  const viewports = [
    { label: "desktop", width: 1440, height: 900 },
    { label: "mobile", width: 390, height: 844 },
  ];

  for (const viewport of viewports) {
    await page.setViewportSize({ width: viewport.width, height: viewport.height });
    for (const target of pages) {
      await page.goto(target.url);
      await page.waitForTimeout(650);
      for (const text of target.must) {
        await page.getByText(text, { exact: false }).first().waitFor({ timeout: 5000 });
      }
      const firstExpand = page.getByRole("button", { name: /展开/ }).first();
      if (await firstExpand.count()) {
        await firstExpand.click();
        await page.waitForTimeout(120);
        for (const text of target.detail) {
          await page.getByText(text, { exact: false }).first().waitFor({ timeout: 5000 });
        }
      }
      if (target.name === "usage-admin") {
        await page.locator('input[data-key="alice-key"][data-limit="5h"]').fill("3");
        await page.getByRole("button", { name: "保存全部" }).click();
        await page.waitForTimeout(250);
        await page.waitForFunction(() => {
          const button = document.querySelector("#saveAllBtn");
          const chip = document.querySelector("#saveChip");
          return button && button.disabled && chip && chip.textContent.includes("无改动");
        }, null, { timeout: 5000 });
      }
      const metricsHaveArc = await page.evaluate(() => getComputedStyle(document.querySelector(".metric"), "::after").content !== "none");
      const layout = await page.evaluate(() => {
        const root = document.scrollingElement || document.documentElement;
        const badButtons = [...document.querySelectorAll("button, .chip, th")]
          .map(el => {
            const rect = el.getBoundingClientRect();
            return {
              text: el.textContent.trim(),
              width: rect.width,
              height: rect.height,
              scrollWidth: el.scrollWidth,
              scrollHeight: el.scrollHeight,
            };
          })
          .filter(item => item.text.length > 0 && item.width > 0 && item.height > 0)
          .filter(item => item.scrollWidth > Math.ceil(item.width) + 2 || item.scrollHeight > Math.ceil(item.height) + 4);
        return {
          pageOverflow: root.scrollWidth > root.clientWidth + 2,
          badButtons,
        };
      });
      if (metricsHaveArc) throw new Error(`${target.name}/${viewport.label}: metric arc is still visible`);
      if (layout.pageOverflow) throw new Error(`${target.name}/${viewport.label}: body has horizontal overflow`);
      if (layout.badButtons.length) throw new Error(`${target.name}/${viewport.label}: clipped control text ${JSON.stringify(layout.badButtons.slice(0, 3))}`);
      results.push(`${target.name}/${viewport.label}: ok`);
    }
  }
  if (consoleErrors.length) {
    throw new Error(`console errors: ${consoleErrors.join(" | ")}`);
  }
  return results;
}
