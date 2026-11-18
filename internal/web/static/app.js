Idiomorph.defaults.ignoreActiveValue = true;
Idiomorph.defaults.callbacks.beforeAttributeUpdated = (name, el, type) =>
  !(name === "open" && el.tagName === "DETAILS") && !(name === "value" && type === "remove");
htmx.config.historyCacheSize = 0;
