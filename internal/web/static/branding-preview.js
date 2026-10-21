// Live preview on the Branding page (design branding §4.3): a drawing of
// the Keycloak login page that follows the form without saving. It
// applies the header rules of internal/branding/header.go: the logo
// (Portico's arch, a file, none), then the header text, or the MSP name
// when there is neither a logo nor a text, then the claim. Colours go in
// through CSS variables set with the CSSOM, which the admin CSP allows.
(function () {
  "use strict";
  var form = document.getElementById("branding-form");
  var pv = document.getElementById("brand-preview");
  if (!form || !pv) {
    return;
  }
  var arch = pv.querySelector(".bp-arch");
  var logo = pv.querySelector(".bp-logo");
  var text = pv.querySelector(".bp-text");
  var claim = pv.querySelector(".bp-claim");
  var fileURL = "";

  function field(name) {
    var el = form.elements[name];
    return el && typeof el.value === "string" ? el.value.trim() : "";
  }

  function source() {
    var r = form.querySelector('input[name="logo_source"]:checked');
    return r ? r.value : "none";
  }

  function update() {
    pv.style.setProperty("--bp-bg", field("background_color"));
    pv.style.setProperty("--bp-text", field("text_color"));
    pv.style.setProperty("--bp-accent", field("accent_color"));
    var src = source();
    var url = "";
    if (src === "file") {
      url = fileURL || (pv.dataset.savedLogo === "1" ? "/settings/branding/logo" : "");
    }
    arch.hidden = src !== "portico";
    if (url) {
      if (logo.getAttribute("src") !== url) {
        logo.src = url;
      }
      logo.hidden = false;
    } else {
      logo.hidden = true;
      logo.removeAttribute("src");
    }
    var t = field("header_text");
    if (!t && arch.hidden && logo.hidden) {
      t = field("msp_name");
    }
    text.textContent = t;
    text.hidden = !t;
    var c = field("tagline");
    claim.textContent = c;
    claim.hidden = !c;
  }

  var file = form.elements["logo"];
  if (file) {
    file.addEventListener("change", function () {
      var f = file.files && file.files[0];
      fileURL = "";
      if (!f) {
        update();
        return;
      }
      var choose = form.querySelector('input[name="logo_source"][value="file"]');
      if (choose) {
        choose.checked = true;
      }
      var reader = new FileReader();
      reader.onload = function () {
        fileURL = String(reader.result);
        update();
      };
      reader.readAsDataURL(f);
    });
  }
  form.addEventListener("input", update);
  form.addEventListener("change", update);
  update();
})();
