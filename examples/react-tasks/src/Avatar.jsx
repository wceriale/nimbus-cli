// A colored circle with someone's initials, derived from their email so it's stable everywhere.
export default function Avatar({ email }) {
  return (
    <span className="avatar" style={{ "--hue": hue(email) }} title={email} aria-hidden="true">
      {initials(email)}
    </span>
  );
}

// "lena.ng@acme.com" -> "LN", "priya@acme.com" -> "P"
export function initials(email = "") {
  const parts = email.split("@")[0].split(/[._+-]/).filter(Boolean);
  return parts.slice(0, 2).map((p) => p[0].toUpperCase()).join("") || "?";
}

// "lena.ng@acme.com" -> "lena.ng"
export const shortName = (email = "") => email.split("@")[0];

function hue(email = "") {
  let h = 0;
  for (const ch of email) h = (h * 31 + ch.codePointAt(0)) % 360;
  return h;
}
