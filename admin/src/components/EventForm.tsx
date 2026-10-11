import { unwrap } from "@talanta/shared";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { api, type Schemas } from "../lib/api";
import { describeError, isCode, splitFieldErrors } from "../lib/errors";
import { fromNairobiInput, toNairobiInput } from "../lib/format";
import { formatCount, formatKes } from "../lib/money";
import { keys, STATUS_LABELS, useTemplates, useVenues, type AdminEvent } from "../lib/queries";
import { Alert, Button, SelectField, Swatch, TextField } from "./ui";

const FIELDS = [
  "venue_id",
  "title",
  "competition",
  "home_team",
  "away_team",
  "kickoff_at",
  "gates_open_at",
  "poster_url",
  "sales_open_at",
  "sales_close_at",
  "max_tickets_per_user",
  "template_id",
] as const;
type FieldName = (typeof FIELDS)[number];

interface FormValues {
  title: string;
  competition: string;
  homeTeam: string;
  awayTeam: string;
  kickoff: string;
  gatesOpen: string;
  salesOpen: string;
  salesClose: string;
  maxTickets: string;
  posterUrl: string;
}

function valuesOf(event: AdminEvent | undefined): FormValues {
  return {
    title: event?.title ?? "",
    competition: event?.competition ?? "",
    homeTeam: event?.home_team ?? "",
    awayTeam: event?.away_team ?? "",
    kickoff: toNairobiInput(event?.kickoff_at),
    gatesOpen: toNairobiInput(event?.gates_open_at),
    salesOpen: toNairobiInput(event?.sales_open_at),
    salesClose: toNairobiInput(event?.sales_close_at),
    maxTickets: String(event?.max_tickets_per_user ?? 6),
    posterUrl: event?.poster_url ?? "",
  };
}

type LocalErrors = Partial<Record<FieldName, string>>;

/** Reads the form into the nullable shape the API takes; reports what is plainly wrong first. */
function readForm(values: FormValues) {
  const errors: LocalErrors = {};
  const text = (value: string) => value.trim() || null;
  const time = (value: string, field: FieldName, required: boolean) => {
    if (!value.trim()) {
      if (required) errors[field] = "Pick a date and time.";
      return null;
    }
    const iso = fromNairobiInput(value);
    if (!iso) errors[field] = "This is not a date and time we can read. Pick it again.";
    return iso;
  };

  if (!values.title.trim()) errors.title = "Give the event a title, for example Harambee Stars vs Uganda Cranes.";
  const kickoff = time(values.kickoff, "kickoff_at", true);
  const gates = time(values.gatesOpen, "gates_open_at", false);
  const salesOpen = time(values.salesOpen, "sales_open_at", false);
  const salesClose = time(values.salesClose, "sales_close_at", false);
  if (kickoff && gates && gates >= kickoff) errors.gates_open_at = "Gates must open before kickoff.";
  if (salesOpen && salesClose && salesClose <= salesOpen) errors.sales_close_at = "Sales must close after they open.";
  if (kickoff && salesClose && salesClose > kickoff) errors.sales_close_at = "Sales cannot close after kickoff.";
  const max = /^\d{1,2}$/.test(values.maxTickets.trim()) ? Number.parseInt(values.maxTickets.trim(), 10) : Number.NaN;
  if (!(max >= 1 && max <= 50)) errors.max_tickets_per_user = "Enter a whole number from 1 to 50.";

  return {
    errors,
    body: {
      title: values.title.trim(),
      competition: text(values.competition),
      home_team: text(values.homeTeam),
      away_team: text(values.awayTeam),
      kickoff_at: kickoff ?? "",
      gates_open_at: gates,
      poster_url: text(values.posterUrl),
      sales_open_at: salesOpen,
      sales_close_at: salesClose,
      max_tickets_per_user: max,
    },
  };
}

interface EventFormProps {
  /** The event being edited. Leave out to create one. */
  event?: AdminEvent;
  /** Why the form cannot be saved right now (cancelled event, publish running). */
  lockedReason?: string | null;
  onSaved: (event: AdminEvent) => void;
}

export function EventForm({ event, lockedReason, onSaved }: EventFormProps) {
  const queryClient = useQueryClient();
  const creating = !event;
  const [values, setValues] = useState<FormValues>(() => valuesOf(event));
  const [local, setLocal] = useState<LocalErrors>({});
  const [venueChoice, setVenueChoice] = useState<number | null>(null);
  const [templateId, setTemplateId] = useState<number | null | undefined>(undefined);
  const [savedAt, setSavedAt] = useState<number | null>(null);

  const venues = useVenues();
  const venueId = event?.venue_id ?? venueChoice ?? venues.data?.items[0]?.id;
  const templates = useTemplates(creating ? venueId : undefined);
  // Until the organiser chooses, the first saved template is offered: it is the quick way.
  const chosenTemplate = templateId === undefined ? (templates.data?.items[0]?.id ?? null) : templateId;

  const save = useMutation({
    mutationFn: async (body: ReturnType<typeof readForm>["body"]) => {
      if (event) {
        // Send only what changed, so an untouched field can never be overwritten by a stale form.
        const changed: Schemas["UpdateEvent"] = {};
        const before = readForm(valuesOf(event)).body;
        for (const key of Object.keys(body) as (keyof typeof body)[]) {
          if (body[key] !== before[key]) (changed as Record<string, unknown>)[key] = body[key];
        }
        if (Object.keys(changed).length === 0) return event;
        return unwrap(api.PATCH("/admin/events/{eventId}", { params: { path: { eventId: event.id } }, body: changed }));
      }
      return unwrap(api.POST("/admin/events", { body: { ...body, venue_id: venueId!, template_id: chosenTemplate } }));
    },
    onSuccess: (saved) => {
      queryClient.setQueryData(keys.event(saved.id), saved);
      setSavedAt(Date.now());
      onSaved(saved);
      return Promise.all([
        queryClient.invalidateQueries({ queryKey: ["events", "list"] }),
        queryClient.invalidateQueries({ queryKey: keys.readiness(saved.id) }),
      ]);
    },
  });

  function set<K extends keyof FormValues>(key: K, value: string) {
    setValues((current) => ({ ...current, [key]: value }));
    setSavedAt(null);
    if (save.isError) save.reset();
  }

  function submit(formEvent: FormEvent) {
    formEvent.preventDefault();
    if (save.isPending || lockedReason) return;
    const { errors, body } = readForm(values);
    if (creating && venueId === undefined) errors.venue_id = "Pick the venue.";
    setLocal(errors);
    if (Object.keys(errors).length > 0) return;
    save.mutate(body);
  }

  const { fields, other } = splitFieldErrors(save.error, FIELDS);
  const errorFor = (field: FieldName) => local[field] ?? fields[field] ?? null;
  const formError =
    save.error && Object.keys(fields).length === 0
      ? describeError(save.error, {
          EVENT_NOT_EDITABLE: isCode(save.error, "EVENT_NOT_EDITABLE")
            ? `This event is ${statusWord(save.error.details)} and can no longer be changed.`
            : undefined,
          VENUE_MISMATCH: "That template belongs to a different venue. Pick another template.",
          NOT_FOUND: creating ? "The venue or template no longer exists. Reload the page and try again." : "This event no longer exists.",
        })
      : null;
  const disabled = Boolean(lockedReason);

  return (
    <form className="stack" onSubmit={submit} noValidate>
      {lockedReason && <Alert tone="info">{lockedReason}</Alert>}

      <fieldset className="formgrid" disabled={disabled}>
        <legend className="formgrid__legend">The match</legend>
        <div className="formgrid__wide">
          <TextField
            label="Title"
            name="title"
            maxLength={200}
            required
            placeholder="Harambee Stars vs Uganda Cranes"
            value={values.title}
            onChange={(e) => set("title", e.target.value)}
            error={errorFor("title")}
          />
        </div>
        <TextField label="Home team" name="home_team" maxLength={100} value={values.homeTeam} onChange={(e) => set("homeTeam", e.target.value)} error={errorFor("home_team")} />
        <TextField label="Away team" name="away_team" maxLength={100} value={values.awayTeam} onChange={(e) => set("awayTeam", e.target.value)} error={errorFor("away_team")} />
        <TextField label="Competition" name="competition" maxLength={200} placeholder="AFCON Qualifier" value={values.competition} onChange={(e) => set("competition", e.target.value)} error={errorFor("competition")} />
        {creating && (venues.data?.items.length ?? 0) > 1 ? (
          <SelectField label="Venue" name="venue_id" value={venueId ?? ""} onChange={(e) => { setVenueChoice(Number(e.target.value)); setTemplateId(undefined); }} error={errorFor("venue_id")}>
            {venues.data?.items.map((venue) => (
              <option key={venue.id} value={venue.id}>
                {venue.name}
              </option>
            ))}
          </SelectField>
        ) : (
          <div className="field">
            <span className="field__label">Venue</span>
            <p className="field__static">
              {event?.venue_name ?? venues.data?.items[0]?.name ?? (venues.isError ? "Could not load the venue" : "Loading…")}
            </p>
            {errorFor("venue_id") && <p className="field__error" role="alert">{errorFor("venue_id")}</p>}
          </div>
        )}
      </fieldset>

      <fieldset className="formgrid" disabled={disabled}>
        <legend className="formgrid__legend">Dates and times, in Nairobi time</legend>
        <TextField label="Kickoff" name="kickoff_at" type="datetime-local" required value={values.kickoff} onChange={(e) => set("kickoff", e.target.value)} error={errorFor("kickoff_at")} />
        <TextField label="Gates open (optional)" name="gates_open_at" type="datetime-local" value={values.gatesOpen} onChange={(e) => set("gatesOpen", e.target.value)} error={errorFor("gates_open_at")} hint="Printed on the ticket. Must be before kickoff." />
        <TextField label="Ticket sales open" name="sales_open_at" type="datetime-local" value={values.salesOpen} onChange={(e) => set("salesOpen", e.target.value)} error={errorFor("sales_open_at")} hint="Needed before the event can be published." />
        <TextField label="Ticket sales close" name="sales_close_at" type="datetime-local" value={values.salesClose} onChange={(e) => set("salesClose", e.target.value)} error={errorFor("sales_close_at")} hint="After sales open, and not after kickoff." />
      </fieldset>

      <fieldset className="formgrid" disabled={disabled}>
        <legend className="formgrid__legend">Limits</legend>
        <TextField label="Most tickets one fan can buy" name="max_tickets_per_user" inputMode="numeric" maxLength={2} value={values.maxTickets} onChange={(e) => set("maxTickets", e.target.value)} error={errorFor("max_tickets_per_user")} hint="From 1 to 50." />
        <TextField label="Poster image address (optional)" name="poster_url" type="url" maxLength={2048} placeholder="https://…" value={values.posterUrl} onChange={(e) => set("posterUrl", e.target.value)} error={errorFor("poster_url")} />
      </fieldset>

      {creating && (
        <div className="field">
          <fieldset className="choice-group choice-group--stacked" disabled={disabled} aria-describedby={errorFor("template_id") ? "template-error" : undefined}>
            <legend>Start from a saved venue template</legend>
            {templates.isPending && venueId !== undefined && <p className="small muted">Loading templates…</p>}
            {templates.isError && (
              <Alert tone="warning">
                The templates did not load ({describeError(templates.error)}).{" "}
                <button type="button" className="link-button" onClick={() => templates.refetch()}>
                  Try again
                </button>{" "}
                or carry on without one.
              </Alert>
            )}
            {templates.data?.items.map((template) => (
              <label key={template.id} className="choice">
                <input type="radio" name="template_id" checked={chosenTemplate === template.id} onChange={() => setTemplateId(template.id)} />
                <span>
                  <span className="choice__title">{template.name}</span>
                  <span className="choice__text">
                    Brings {template.categories.length} {template.categories.length === 1 ? "category" : "categories"} with prices, paints{" "}
                    {formatCount(template.block_assignment_count)} blocks and sets {formatCount(template.row_assignment_count)} row overrides. You can
                    change all of it before publishing.
                  </span>
                  <span className="catlist">
                    {template.categories.map((category) => (
                      <span key={category.name} className="catlist__item">
                        <Swatch color={category.color_hex} />
                        {category.name} · {formatKes(category.price_cents)}
                      </span>
                    ))}
                  </span>
                </span>
              </label>
            ))}
            <label className="choice">
              <input type="radio" name="template_id" checked={chosenTemplate === null} onChange={() => setTemplateId(null)} />
              <span>
                <span className="choice__title">No template</span>
                <span className="choice__text">Start empty: you add the categories and paint every block yourself.</span>
              </span>
            </label>
          </fieldset>
          {errorFor("template_id") && (
            <p className="field__error" id="template-error" role="alert">
              {errorFor("template_id")}
            </p>
          )}
        </div>
      )}

      {formError && (
        <Alert tone="error">
          {formError}
          {other.map((message) => (
            <p key={message}>{message}</p>
          ))}
        </Alert>
      )}
      {!formError && other.length > 0 && (
        <Alert tone="error">
          {other.map((message) => (
            <p key={message}>{message}</p>
          ))}
        </Alert>
      )}

      <div className="form-actions">
        <Button type="submit" loading={save.isPending} disabled={disabled}>
          {creating ? (save.isPending ? "Creating draft…" : "Create draft event") : save.isPending ? "Saving…" : "Save changes"}
        </Button>
        {creating ? (
          <span className="small muted">Nothing goes on sale until you publish.</span>
        ) : (
          savedAt !== null && !save.isPending && <span className="small saved" role="status">Saved.</span>
        )}
      </div>
    </form>
  );
}

function statusWord(details: unknown): string {
  const status = (details as { current_status?: keyof typeof STATUS_LABELS } | null)?.current_status;
  return status ? STATUS_LABELS[status].toLowerCase() : "closed";
}
