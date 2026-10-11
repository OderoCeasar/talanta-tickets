import { unwrap } from "@talanta/shared";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { useLocation } from "react-router-dom";
import { Alert, Button, Card, ConfirmDialog, LoadFailed, SkeletonRows, StateBlock, Swatch, TextField } from "../../components/ui";
import { api, type Schemas } from "../../lib/api";
import { categoryInUseText, describeError, isCode, splitFieldErrors } from "../../lib/errors";
import { centsToShillingsInput, formatCount, formatKes, parseShillingsToCents } from "../../lib/money";
import { isPublished, keys, useAssignments, useCategories, type Category } from "../../lib/queries";
import { editLock, useEventContext } from "./EventPage";

/** The colours a category may take. Chosen to stay apart from each other on the seat map. */
export const CATEGORY_COLOURS: { hex: string; name: string }[] = [
  { hex: "#7C3AED", name: "Violet" },
  { hex: "#2563EB", name: "Blue" },
  { hex: "#0891B2", name: "Teal" },
  { hex: "#059669", name: "Green" },
  { hex: "#65A30D", name: "Lime" },
  { hex: "#C9A227", name: "Gold" },
  { hex: "#D97706", name: "Orange" },
  { hex: "#DC2626", name: "Red" },
  { hex: "#DB2777", name: "Pink" },
  { hex: "#4B5563", name: "Slate" },
];

const FIELDS = ["name", "price_cents", "color_hex", "description"] as const;

interface Draft {
  name: string;
  price: string;
  color: string;
  description: string;
}

function draftOf(category: Category | null, used: string[]): Draft {
  if (category) {
    return {
      name: category.name,
      price: centsToShillingsInput(category.price_cents),
      color: category.color_hex.toUpperCase(),
      description: category.description ?? "",
    };
  }
  const free = CATEGORY_COLOURS.find((colour) => !used.includes(colour.hex));
  return { name: "", price: "", color: (free ?? CATEGORY_COLOURS[0]!).hex, description: "" };
}

export function PricingTab() {
  const context = useEventContext();
  const { event } = context;
  const location = useLocation();
  const justCreated = Boolean((location.state as { created?: boolean } | null)?.created);
  const queryClient = useQueryClient();
  const categories = useCategories(event.id);
  const assignments = useAssignments(event.id);
  const lock = editLock(context, "prices");
  const [editing, setEditing] = useState<Category | "new" | null>(null);
  const [toDelete, setToDelete] = useState<Category | null>(null);
  const formRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (editing) formRef.current?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, [editing]);

  const refresh = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: keys.categories(event.id) }),
      queryClient.invalidateQueries({ queryKey: keys.assignments(event.id) }),
      queryClient.invalidateQueries({ queryKey: keys.readiness(event.id) }),
      queryClient.invalidateQueries({ queryKey: keys.sales(event.id) }),
      queryClient.invalidateQueries({ queryKey: keys.event(event.id) }),
    ]);

  const remove = useMutation({
    mutationFn: (category: Category) =>
      unwrap(api.DELETE("/admin/categories/{categoryId}", { params: { path: { categoryId: category.id } } })),
    onSuccess: () => setToDelete(null),
    onSettled: refresh,
  });

  const rows = categories.data?.items ?? [];
  const paintedWith = (category: Category) =>
    (assignments.data?.blocks.filter((entry) => entry.category_id === category.id).length ?? 0) +
    (assignments.data?.rows.filter((entry) => entry.category_id === category.id).length ?? 0);
  const published = isPublished(event.status);

  const removeError = remove.error
    ? isCode(remove.error, "CATEGORY_IN_USE")
      ? `This category cannot be deleted. ${categoryInUseText(remove.error.details)} Once an event is published its categories stay, because seats and bookings point at them. You can still rename it or change its price.`
      : describeError(remove.error, {
          EVENT_NOT_EDITABLE: "This event is closed, so its categories can no longer be changed.",
          NOT_FOUND: "This category no longer exists.",
        })
    : null;

  return (
    <>
      {justCreated && (
        <Alert tone="success" title="Draft created">
          {rows.length > 0
            ? "The template's categories and prices are below. Check them, then look at Seat painting and Publish."
            : "Add your ticket categories and prices here, then paint the seats."}
        </Alert>
      )}
      {lock && <Alert tone="info">{lock}</Alert>}

      <Card>
        <div className="card__header">
          <h2>Categories and prices</h2>
          <Button variant="secondary" size="small" disabled={Boolean(lock)} onClick={() => setEditing("new")}>
            Add a category
          </Button>
        </div>
        <div className="card__body card__body--flush">
          {categories.isPending ? (
            <SkeletonRows label="Loading categories" />
          ) : categories.isError ? (
            <LoadFailed message={describeError(categories.error)} onRetry={() => categories.refetch()} retrying={categories.isFetching} />
          ) : rows.length === 0 ? (
            <StateBlock title="No categories yet">
              <p>A category is a kind of ticket with one price, such as VIP or Terrace. Add at least one before painting seats.</p>
            </StateBlock>
          ) : (
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th scope="col">Category</th>
                    <th scope="col">Price per ticket</th>
                    <th scope="col">{published ? "Seats left" : "Painted"}</th>
                    <th scope="col">
                      <span className="visually-hidden">Actions</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((category) => (
                    <tr key={category.id}>
                      <td className="table__primary" data-label="Category">
                        <span className="catname">
                          <Swatch color={category.color_hex} />
                          {category.name}
                        </span>
                        {category.description && <span className="table__secondary">{category.description}</span>}
                      </td>
                      <td data-label="Price per ticket">
                        {category.price_cents === 0 ? (
                          <span className="chip chip--pending">No price yet</span>
                        ) : (
                          <strong>{formatKes(category.price_cents)}</strong>
                        )}
                      </td>
                      <td data-label={published ? "Seats left" : "Painted"}>
                        {published
                          ? `${formatCount(category.seats_available)} of ${formatCount(category.seats_total)}`
                          : assignments.data
                            ? paintedSummary(assignments.data, category.id)
                            : "…"}
                      </td>
                      <td className="table__actions-cell" data-label="Actions">
                        <div className="table__actions">
                          <Button variant="secondary" size="small" disabled={Boolean(lock)} onClick={() => setEditing(category)} aria-label={`Edit ${category.name}`}>
                            Edit
                          </Button>
                          <Button
                            variant="danger"
                            size="small"
                            disabled={Boolean(lock)}
                            onClick={() => {
                              remove.reset();
                              setToDelete(category);
                            }}
                            aria-label={`Delete ${category.name}`}
                          >
                            Delete
                          </Button>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      </Card>

      {editing && !lock && (
        <div ref={formRef}>
          <CategoryForm
            key={editing === "new" ? "new" : editing.id}
            eventId={event.id}
            category={editing === "new" ? null : editing}
            usedColours={rows.map((category) => category.color_hex.toUpperCase())}
            draftEvent={event.status === "DRAFT"}
            onDone={() => {
              setEditing(null);
              void refresh();
            }}
            onCancel={() => setEditing(null)}
          />
        </div>
      )}

      <ConfirmDialog
        open={toDelete !== null}
        title={`Delete ${toDelete?.name ?? "this category"}?`}
        confirmLabel={remove.isPending ? "Deleting…" : isCode(remove.error, "EVENT_BUSY") ? "Try again" : "Delete category"}
        cancelLabel="Keep it"
        danger
        busy={remove.isPending}
        error={removeError}
        onConfirm={() => toDelete && remove.mutate(toDelete)}
        onCancel={() => setToDelete(null)}
      >
        {toDelete && !published && paintedWith(toDelete) > 0 ? (
          <p>
            {paintedSummary(assignments.data!, toDelete.id)} are painted with <strong>{toDelete.name}</strong>. They will have no
            category, and the event cannot be published until they are painted again.
          </p>
        ) : (
          <p>
            The category <strong>{toDelete?.name}</strong> and its price will be removed from this event.
          </p>
        )}
      </ConfirmDialog>
    </>
  );
}

function paintedSummary(assignments: Schemas["Assignments"], categoryId: number): string {
  const blocks = assignments.blocks.filter((entry) => entry.category_id === categoryId).length;
  const rows = assignments.rows.filter((entry) => entry.category_id === categoryId).length;
  const parts = [`${formatCount(blocks)} ${blocks === 1 ? "block" : "blocks"}`];
  if (rows > 0) parts.push(`${formatCount(rows)} ${rows === 1 ? "row" : "rows"}`);
  return parts.join(" and ");
}

function CategoryForm({
  eventId,
  category,
  usedColours,
  draftEvent,
  onDone,
  onCancel,
}: {
  eventId: number;
  category: Category | null;
  usedColours: string[];
  /** While the event is a draft a price of 0 can be saved; afterwards the server refuses it. */
  draftEvent: boolean;
  onDone: () => void;
  onCancel: () => void;
}) {
  const [draft, setDraft] = useState<Draft>(() => draftOf(category, usedColours));
  const [localError, setLocalError] = useState<{ name?: string; price?: string }>({});
  /** Set when the server asks "are you sure" about a price change on a published event. */
  const [confirming, setConfirming] = useState<{ sold: number; held: number; body: Schemas["UpdateCategory"] } | null>(null);
  type Changes = Partial<Omit<Schemas["UpdateCategory"], "confirm">>;

  const save = useMutation({
    mutationFn: (request: { create: Schemas["CreateCategory"] } | { update: Schemas["UpdateCategory"] }) =>
      "update" in request
        ? unwrap(api.PATCH("/admin/categories/{categoryId}", { params: { path: { categoryId: category!.id } }, body: request.update }))
        : unwrap(api.POST("/admin/events/{eventId}/categories", { params: { path: { eventId } }, body: request.create })),
    onSuccess: () => {
      setConfirming(null);
      onDone();
    },
    onError: (error, request) => {
      // Nothing was changed. Ask the question with the server's own counts, then resend.
      if (isCode(error, "CONFIRMATION_REQUIRED") && "update" in request) {
        const counts = error.details as { sold?: number; held?: number } | null;
        setConfirming({ sold: counts?.sold ?? 0, held: counts?.held ?? 0, body: request.update });
      }
    },
  });

  // Offer the fixed set, plus this category's own colour if a template brought a different one.
  const colours = CATEGORY_COLOURS.some((colour) => colour.hex === draft.color) || !category
    ? CATEGORY_COLOURS
    : [{ hex: category.color_hex.toUpperCase(), name: "Current colour" }, ...CATEGORY_COLOURS];

  function change(patch: Partial<Draft>) {
    setDraft((current) => ({ ...current, ...patch }));
    setLocalError({});
    if (save.isError) save.reset();
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    if (save.isPending) return;
    const errors: { name?: string; price?: string } = {};
    const name = draft.name.trim();
    if (!name) errors.name = "Give the category a name, for example VIP.";
    // The one place shillings become cents.
    const price = parseShillingsToCents(draft.price);
    if (!price.ok) errors.price = price.message;
    setLocalError(errors);
    if (!price.ok || errors.name) return;
    const description = draft.description.trim() || null;

    if (!category) {
      save.mutate({ create: { name, price_cents: price.cents, color_hex: draft.color, description } });
      return;
    }
    const body: Changes = {};
    if (name !== category.name) body.name = name;
    if (price.cents !== category.price_cents) body.price_cents = price.cents;
    if (draft.color !== category.color_hex.toUpperCase()) body.color_hex = draft.color;
    if (description !== (category.description ?? null)) body.description = description;
    if (Object.keys(body).length === 0) {
      onCancel();
      return;
    }
    // Never confirmed on the first try: the server then says whether the question is needed.
    save.mutate({ update: body });
  }

  const { fields, other } = splitFieldErrors(save.error, FIELDS);
  const nameTaken = isCode(save.error, "ALREADY_EXISTS") ? "This event already has a category with this name." : null;
  const asking = isCode(save.error, "CONFIRMATION_REQUIRED");
  const formError =
    save.error && !asking && !nameTaken && Object.keys(fields).length === 0
      ? describeError(save.error, {
          EVENT_NOT_EDITABLE: "This event is closed, so its categories can no longer be changed.",
          NOT_FOUND: "This category no longer exists. Close this form and reload the page.",
        })
      : null;
  const parsed = parseShillingsToCents(draft.price);

  return (
    <Card>
      <div className="card__header">
        <h2>{category ? `Edit ${category.name}` : "Add a category"}</h2>
      </div>
      <form className="card__body stack" onSubmit={submit} noValidate>
        <div className="formgrid">
          <TextField
            label="Name"
            name="name"
            maxLength={60}
            required
            placeholder="VIP"
            value={draft.name}
            onChange={(e) => change({ name: e.target.value })}
            error={localError.name ?? fields.name ?? nameTaken}
          />
          <TextField
            label="Price per ticket, in shillings"
            name="price"
            inputMode="decimal"
            autoComplete="off"
            required
            placeholder="1500"
            value={draft.price}
            onChange={(e) => change({ price: e.target.value })}
            error={localError.price ?? fields.price_cents}
            hint={
              parsed.ok
                ? parsed.cents === 0
                  ? draftEvent
                    ? "A price of 0 can be saved, but the event cannot be published until it is above 0."
                    : "This event is published, so the price must be above 0."
                  : `Fans will pay ${formatKes(parsed.cents)} for each ticket.`
                : "For example 1500 or 1,500. Cents are allowed: 1500.50."
            }
          />
          <div className="formgrid__wide">
            <TextField
              label="Description (optional)"
              name="description"
              maxLength={500}
              placeholder="West stand, padded seats"
              value={draft.description}
              onChange={(e) => change({ description: e.target.value })}
              error={fields.description}
            />
          </div>
          <div className="formgrid__wide field">
            <fieldset className="swatches" aria-describedby={fields.color_hex ? "colour-error" : undefined}>
              <legend>Colour on the seat map</legend>
              {colours.map((colour) => (
                <label key={colour.hex} className="swatches__option" title={colour.name}>
                  <input
                    type="radio"
                    name="color_hex"
                    value={colour.hex}
                    checked={draft.color === colour.hex}
                    onChange={() => change({ color: colour.hex })}
                  />
                  <span className="swatches__chip" style={{ background: colour.hex }} />
                  <span className="visually-hidden">
                    {colour.name}
                    {usedColours.includes(colour.hex) && colour.hex !== category?.color_hex.toUpperCase() ? " (used by another category)" : ""}
                  </span>
                </label>
              ))}
            </fieldset>
            {fields.color_hex && (
              <p className="field__error" id="colour-error" role="alert">
                {fields.color_hex}
              </p>
            )}
            {usedColours.includes(draft.color) && draft.color !== category?.color_hex.toUpperCase() && (
              <p className="field__hint">Another category already uses this colour. They will look the same on the map.</p>
            )}
          </div>
        </div>

        {(formError || (!asking && other.length > 0)) && (
          <Alert tone="error">
            {formError}
            {other.map((message) => (
              <p key={message}>{message}</p>
            ))}
          </Alert>
        )}

        <div className="form-actions">
          <Button type="submit" loading={save.isPending && !confirming}>
            {category ? "Save category" : "Add category"}
          </Button>
          <Button variant="ghost" onClick={onCancel} disabled={save.isPending}>
            Cancel
          </Button>
        </div>
      </form>

      <ConfirmDialog
        open={confirming !== null}
        title="Change the price of tickets already on sale?"
        confirmLabel={save.isPending ? "Changing…" : confirming?.body.price_cents !== undefined ? `Change to ${formatKes(confirming.body.price_cents)}` : "Change price"}
        cancelLabel="Keep the old price"
        busy={save.isPending}
        error={save.error && !asking ? describeError(save.error) : null}
        onConfirm={() => confirming && save.mutate({ update: { ...confirming.body, confirm: true } })}
        onCancel={() => {
          setConfirming(null);
          save.reset();
        }}
      >
        <p>
          This event is already published, so fans may be looking at{" "}
          <strong>{category ? formatKes(category.price_cents) : "the old price"}</strong> for {category?.name} right now.
        </p>
        <ul className="plainlist">
          <li>
            <strong>Past bookings keep their old price.</strong>{" "}
            {confirming && confirming.sold + confirming.held > 0
              ? `${formatCount(confirming.sold)} sold and ${formatCount(confirming.held)} held ${confirming.sold + confirming.held === 1 ? "ticket is" : "tickets are"} not affected.`
              : "No tickets in this category are sold or held yet."}
          </li>
          <li>Every ticket bought from now on costs the new price.</li>
        </ul>
      </ConfirmDialog>
    </Card>
  );
}
