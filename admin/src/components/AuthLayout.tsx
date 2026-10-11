import type { ReactNode } from "react";
import { BrandMark } from "./icons";

/** The frame around the signed-out pages: brand panel on the left, the form on the right. */
export function AuthLayout({ children }: { children: ReactNode }) {
  return (
    <div className="auth">
      <aside className="auth__aside">
        <svg className="auth__art" viewBox="0 0 600 420" fill="none" aria-hidden="true" focusable="false">
          {[200, 165, 130, 95].map((ry, i) => (
            <ellipse key={ry} cx="300" cy="210" rx={ry * 1.42} ry={ry} stroke="var(--champagne)" strokeWidth={i === 0 ? 3 : 2} />
          ))}
          <rect x="222" y="162" width="156" height="96" rx="6" stroke="var(--champagne)" strokeWidth="2" />
          <path d="M300 162v96" stroke="var(--champagne)" strokeWidth="2" />
          <circle cx="300" cy="210" r="16" stroke="var(--champagne)" strokeWidth="2" />
        </svg>
        <span className="brand">
          <BrandMark />
          <span>
            <span className="brand__name">Talanta Tickets</span>
            <span className="brand__tag">Organiser site</span>
          </span>
        </span>
        <div className="auth__pitch">
          <h2>Every seat in the stadium, in one place.</h2>
          <p>Set up events, lay out the stands, set prices and watch sales as they happen.</p>
        </div>
        <p className="auth__footnote">Talanta Stadium, Nairobi</p>
      </aside>
      <main className="auth__main">{children}</main>
    </div>
  );
}
