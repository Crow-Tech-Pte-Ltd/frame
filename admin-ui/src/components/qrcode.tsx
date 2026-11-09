// QR code drawn as one SVG <path> from qrcode-generator's module matrix.
// No innerHTML, no canvas, no data: URL — works under the strict CSP.
import * as React from "react";
import qrcode from "qrcode-generator";

export function QRCode({ value, label, size = 224 }: { value: string; label: string; size?: number }) {
  const { d, n } = React.useMemo(() => {
    const qr = qrcode(0, "M");
    qr.addData(value, "Byte");
    qr.make();
    const count = qr.getModuleCount();
    let path = "";
    for (let r = 0; r < count; r++) {
      for (let c = 0; c < count; c++) {
        if (qr.isDark(r, c)) path += `M${c + 4} ${r + 4}h1v1h-1z`;
      }
    }
    return { d: path, n: count + 8 };
  }, [value]);
  return (
    <svg
      role="img"
      aria-label={label}
      viewBox={`0 0 ${n} ${n}`}
      width={size}
      height={size}
      shapeRendering="crispEdges"
      className="h-auto max-w-full rounded-md border-2 border-primary-400 bg-surface-50"
    >
      <rect width={n} height={n} fill="#ffffff" />
      <path d={d} fill="#000000" />
    </svg>
  );
}
