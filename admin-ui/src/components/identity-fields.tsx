// Display name / username / email-or-phone fields shared by Setup and
// "New user".

import { cn } from "@/lib/utils";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Field } from "@/components/common";
import { identifierError, usernameError } from "@/lib/validate";

export type Identity = {
  display_name: string;
  username: string;
  method: "email" | "phone";
  identifier: string;
};

export const emptyIdentity: Identity = { display_name: "", username: "", method: "email", identifier: "" };

export function identityErrors(v: Identity, usernameRequired: boolean): Partial<Record<keyof Identity, string>> {
  const e: Partial<Record<keyof Identity, string>> = {};
  const name = v.display_name.trim();
  if (name.length < 1 || name.length > 40) e.display_name = "Use 1–40 characters";
  if (usernameRequired || v.username !== "") {
    const u = usernameError(v.username);
    if (u) e.username = u;
  }
  const i = identifierError(v.method, v.identifier);
  if (i) e.identifier = i;
  return e;
}

export function IdentityFields({
  idPrefix,
  value,
  onChange,
  errors,
  usernameRequired,
}: {
  idPrefix: string;
  value: Identity;
  onChange: (v: Identity) => void;
  errors: Partial<Record<keyof Identity, string>>;
  usernameRequired: boolean;
}) {
  const set = (patch: Partial<Identity>) => onChange({ ...value, ...patch });
  const err = (k: keyof Identity) =>
    errors[k] ? (
      <p id={`${idPrefix}-${k}-err`} className="text-xs font-medium text-destructive">
        {errors[k]}
      </p>
    ) : null;
  return (
    <>
      <Field id={`${idPrefix}-name`} label="Display name">
        <Input
          id={`${idPrefix}-name`}
          autoComplete="name"
          required
          maxLength={40}
          aria-invalid={!!errors.display_name}
          aria-describedby={errors.display_name ? `${idPrefix}-display_name-err` : undefined}
          value={value.display_name}
          onChange={(e) => set({ display_name: e.target.value })}
        />
        {err("display_name")}
      </Field>
      <Field
        id={`${idPrefix}-username`}
        label={usernameRequired ? "Username" : "Username (optional)"}
        hint="3–20 characters: a–z, 0–9, . and _"
      >
        <div className="flex items-center rounded-md border border-input bg-background shadow-sm focus-within:ring-2 focus-within:ring-ring">
          <span className="pl-3 font-azeret text-sm text-muted-foreground" aria-hidden="true">
            @
          </span>
          <input
            id={`${idPrefix}-username`}
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            required={usernameRequired}
            maxLength={20}
            aria-invalid={!!errors.username}
            aria-describedby={`${idPrefix}-username-hint${errors.username ? ` ${idPrefix}-username-err` : ""}`}
            value={value.username}
            onChange={(e) => set({ username: e.target.value.toLowerCase().replace(/\s/g, "") })}
            className="h-10 w-full rounded-md bg-transparent px-1 font-azeret text-base focus:outline-none sm:text-sm"
          />
        </div>
        {err("username")}
      </Field>
      <fieldset className="grid gap-2">
        <legend className="mb-2 font-azeret text-xs font-bold uppercase tracking-wide">Sign-in with</legend>
        <div className="inline-flex w-fit rounded-md border-2 border-primary p-0.5" role="radiogroup" aria-label="Sign-in method">
          {(["email", "phone"] as const).map((m) => (
            <label
              key={m}
              className={cn(
                "cursor-pointer rounded-sm px-3 py-1.5 font-azeret text-xs font-bold focus-within:ring-2 focus-within:ring-ring",
                value.method === m ? "bg-primary text-primary-foreground" : "hover:bg-soft",
              )}
            >
              <input
                type="radio"
                name={`${idPrefix}-method`}
                value={m}
                checked={value.method === m}
                onChange={() => set({ method: m, identifier: "" })}
                className="sr-only"
              />
              {m === "email" ? "Email" : "Phone"}
            </label>
          ))}
        </div>
        <Label htmlFor={`${idPrefix}-identifier`} className="mt-2">
          {value.method === "email" ? "Email address" : "Phone number"}
        </Label>
        <Input
          id={`${idPrefix}-identifier`}
          type={value.method === "email" ? "email" : "tel"}
          autoComplete={value.method === "email" ? "email" : "tel"}
          autoCapitalize="none"
          spellCheck={false}
          required
          placeholder={value.method === "email" ? "you@example.com" : "+34600111222"}
          aria-invalid={!!errors.identifier}
          aria-describedby={errors.identifier ? `${idPrefix}-identifier-err` : undefined}
          value={value.identifier}
          onChange={(e) => set({ identifier: e.target.value })}
        />
        {err("identifier")}
      </fieldset>
    </>
  );
}
