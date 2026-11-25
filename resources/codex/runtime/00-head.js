/**
 * antigravity-codex-theme · Browser Runtime Bundle
 * Part 00: Bootstrap, Lifecycle Cleanup, Theme Variables & Stylesheet Injection
 */
((boot) => {
  const IS_SWARM_PLUGIN_FRAME = Boolean(
    window.top !== window &&
      window.location &&
      window.location.protocol === 'plugin:' &&
      /^swarm--/i.test(window.location.hostname || '')
  );
  if (window.top !== window && !IS_SWARM_PLUGIN_FRAME) return;
  if (!boot || typeof boot !== 'object') return;

  const PATCH_VERSION = boot.version || '1.3.1';
  const ENABLED = boot.enabled !== false;
  const CSS_TEXT = boot.cssText || '';
  const CUSTOM_CSS_TEXT = boot.customCss || '';
  const THEME_VARS = boot.themeVars && typeof boot.themeVars === 'object' ? boot.themeVars : {};
  const MODULES = Object.assign(
    {
      theme: true,
      customFont: true,
      metrics: true,
      deadlockHealer: true,
      zoomShortcuts: true,
      cdpAutoRelaunch: true,
      customCss: true,
      customJs: true
    },
    boot.modules || {}
  );

  const DEFAULT_PRESET = boot.preset || 'codex-pure-white';

  const SELECTORS = Object.assign(
    {
      inputBoxId: 'antigravity.agentSidePanelInputBox',
      branchPillInBox:
        '[data-testid="branch-pill-trigger"], [data-testid="project-branch-picker"], [data-testid="project-selector-trigger"], [data-testid="location-selector-trigger"], [aria-label^="Select project"], [aria-label^="选择项目"]',
      homeGreeting: '[data-testid="home-greeting"], [data-testid="home-splash-content"]'
    },
    boot.selectors || {}
  );

  if (Array.isArray(window.__ag_cleanup_fns) && window.__ag_cleanup_fns.length > 0) {
    const prevCleanups = window.__ag_cleanup_fns.splice(0, window.__ag_cleanup_fns.length);
    for (let i = 0; i < prevCleanups.length; i++) {
      try {
        if (typeof prevCleanups[i] === 'function') prevCleanups[i]();
      } catch (e) {}
    }
  }
  window.__ag_cleanup_fns = [];
  if (window.__ag_dcl_handler) {
    try {
      window.removeEventListener('DOMContentLoaded', window.__ag_dcl_handler);
    } catch (e) {}
    window.__ag_dcl_handler = null;
  }
  if (window.__ag_turn_metrics_observer) {
    try {
      window.__ag_turn_metrics_observer.disconnect();
    } catch (e) {}
    window.__ag_turn_metrics_observer = null;
  }

  function applyThemeVars(varsObj) {
    const prevKeys = Array.isArray(window.__ag_applied_theme_vars) ? window.__ag_applied_theme_vars : [];
    for (let i = 0; i < prevKeys.length; i++) {
      const k = prevKeys[i];
      document.documentElement.style.removeProperty(k);
      if (document.body) document.body.style.removeProperty(k);
    }
    window.__ag_applied_theme_vars = [];

    const keys = Object.keys(varsObj || {});
    const globalDecls = [];
    const lightDecls = [];
    const darkDecls = [];

    for (let i = 0; i < keys.length; i++) {
      const k = keys[i];
      const v = varsObj[k];
      if (!k.startsWith('--') || v === null || v === undefined || String(v).trim() === '') continue;
      const cleanVal = String(v).trim().replace(/[;{}]/g, '');
      if (k.startsWith('--ag-dark-')) {
        darkDecls.push(`${k}: ${cleanVal} !important;`);
      } else if (k.startsWith('--ag-')) {
        globalDecls.push(`${k}: ${cleanVal} !important;`);
      } else {
        lightDecls.push(`${k}: ${cleanVal} !important;`);
      }
    }

    let varStyle = document.getElementById('ag-user-theme-vars');
    if (globalDecls.length === 0 && lightDecls.length === 0 && darkDecls.length === 0) {
      if (varStyle) varStyle.remove();
    } else {
      const head = document.head || document.getElementsByTagName('head')[0] || document.documentElement;
      if (!varStyle) {
        varStyle = document.createElement('style');
        varStyle.id = 'ag-user-theme-vars';
      }
      if (head) {
        const customStyleEl = document.getElementById('ag-user-custom-style');
        if (customStyleEl && customStyleEl.parentNode === head) {
          head.insertBefore(varStyle, customStyleEl);
        } else if (varStyle.parentNode !== head) {
          head.appendChild(varStyle);
        }
      }
      const blocks = [];
      if (globalDecls.length > 0) {
        blocks.push(
          `:root, html[data-ag-os]:not([data-ag-disabled]), html[data-ag-os]:not([data-ag-disabled]) body { ${globalDecls.join(' ')} }`
        );
      }
      if (lightDecls.length > 0) {
        blocks.push(
          `html[data-ag-os]:not([data-ag-theme-mode="dark"]):not(:has(> body.dark)), html[data-ag-os]:not([data-ag-theme-mode="dark"]) body:not(.dark) { ${lightDecls.join(' ')} }`
        );
      }
      if (darkDecls.length > 0) {
        blocks.push(
          `html[data-ag-os]:not([data-ag-disabled])[data-ag-theme-mode="dark"], html[data-ag-os]:not([data-ag-disabled])[data-ag-theme-mode="dark"] body, html[data-ag-os]:not([data-ag-disabled]):has(> body.dark), html[data-ag-os]:not([data-ag-disabled]) body.dark { ${darkDecls.join(' ')} }`
        );
      }
      const nextCss = blocks.join('\n');
      if (varStyle.textContent !== nextCss) {
        varStyle.textContent = nextCss;
      }
    }

    if (varsObj && varsObj['--ag-chat-font-size']) {
      document.documentElement.setAttribute('data-ag-custom-font-size', 'true');
    } else {
      document.documentElement.removeAttribute('data-ag-custom-font-size');
    }
  }

  if (!ENABLED) {
    for (const id of [
      'ag-codex-theme',
      'ag-user-theme-vars',
      'ag-user-custom-style',
      'ag-live-metrics-pill',
      'ag-side-metrics-pill',
      'ag-live-metrics-tooltip',
      'ag-turn-metrics-tooltip',
      'ag-settings-nav-divider',
      'ag-settings-nav-wrap',
      'ag-settings-pane',
      'ag-settings-model-menu',
      'ag-settings-effort-menu'
    ]) {
      const el = document.getElementById(id);
      if (el) el.remove();
    }
    document.querySelectorAll('.ag-turn-metrics, .ag-live-metrics-pill, .ag-prompt-ghost-overlay').forEach((el) => el.remove());
    document.querySelectorAll('[data-ag-has-suggestion]').forEach((el) => el.removeAttribute('data-ag-has-suggestion'));
    document.querySelectorAll('[data-ag-codex-settings-open]').forEach((el) => el.removeAttribute('data-ag-codex-settings-open'));
    document.querySelectorAll('[data-ag-settings-right-pane]').forEach((el) => el.removeAttribute('data-ag-settings-right-pane'));
    applyThemeVars({});
    for (const attr of ['data-ag-preset', 'data-ag-theme-mode', 'data-ag-force-mode', 'data-ag-page-type', 'data-ag-os', 'data-ag-custom-font', 'data-ag-custom-font-size']) {
      document.documentElement.removeAttribute(attr);
    }
    window.__ag_patch_version = PATCH_VERSION + '-disabled';
    return;
  }

  const headTarget = document.head || document.getElementsByTagName('head')[0] || document.documentElement;
  let style = document.getElementById('ag-codex-theme');
  if (!style) {
    style = document.createElement('style');
    style.id = 'ag-codex-theme';
    if (headTarget) headTarget.appendChild(style);
  }
  const nextCssText = CSS_TEXT || '/* antigravity-codex-theme active */';
  if (style && style.textContent !== nextCssText) {
    style.textContent = nextCssText;
  }

  const detectedOs =
    boot.platform || (/Win/i.test(navigator.platform || '') ? 'win32' : /Mac/i.test(navigator.platform || '') ? 'darwin' : 'linux');
  document.documentElement.setAttribute('data-ag-os', detectedOs);
  if (IS_SWARM_PLUGIN_FRAME) {
    document.documentElement.setAttribute('data-ag-page-type', 'plugin');
  }
  if (MODULES.theme !== false) {
    applyThemeVars(THEME_VARS);
    if (MODULES.customFont === true) {
      document.documentElement.setAttribute('data-ag-custom-font', 'true');
    } else {
      document.documentElement.removeAttribute('data-ag-custom-font');
    }
  } else {
    applyThemeVars({});
    for (const attr of ['data-ag-preset', 'data-ag-theme-mode', 'data-ag-force-mode', 'data-ag-custom-font', 'data-ag-custom-font-size']) {
      document.documentElement.removeAttribute(attr);
    }
  }

  let userStyle = document.getElementById('ag-user-custom-style');
  if (MODULES.customCss && CUSTOM_CSS_TEXT.trim()) {
    if (!userStyle) {
      userStyle = document.createElement('style');
      userStyle.id = 'ag-user-custom-style';
      if (headTarget) headTarget.appendChild(userStyle);
    }
    if (userStyle.textContent !== CUSTOM_CSS_TEXT) {
      userStyle.textContent = CUSTOM_CSS_TEXT;
    }
  } else if (userStyle) {
    userStyle.remove();
  }

  function ensureStylesAtEndOfHead() {
    const h = document.head || document.getElementsByTagName('head')[0];
    if (!h) return;
    const base = document.getElementById('ag-codex-theme');
    if (!base) return;
    let needsMove = base.parentNode !== h;
    if (!needsMove) {
      for (let sib = base.nextElementSibling; sib; sib = sib.nextElementSibling) {
        if (
          sib.id !== 'ag-user-theme-vars' &&
          sib.id !== 'ag-user-custom-style' &&
          (sib.tagName === 'LINK' || sib.tagName === 'STYLE')
        ) {
          needsMove = true;
          break;
        }
      }
    }
    if (needsMove) {
      for (const id of ['ag-codex-theme', 'ag-user-theme-vars', 'ag-user-custom-style']) {
        const el = document.getElementById(id);
        if (el) h.appendChild(el);
      }
    }
  }
  ensureStylesAtEndOfHead();
