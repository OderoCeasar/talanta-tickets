import { ApiError } from "@talanta/shared";
import * as Crypto from "expo-crypto";
import { router, useIsFocused, useLocalSearchParams } from "expo-router";
import { useCallback, useEffect, useRef, useState } from "react";
import { Pressable, StyleSheet, Text, View } from "react-native";

import { useBestAvailable, useCategories, useEvent } from "../../../src/api/queries";
import { writeStoredCheckout } from "../../../src/api/storage";
import { errorMessage, isEventBusy, isRetryable } from "../../../src/lib/errors";
import { formatCount, formatKes } from "../../../src/lib/money";
import { useNow } from "../../../src/lib/useNow";
import { useSession } from "../../../src/session";
import { useBookingFlow } from "../../../src/store/bookingFlow";
import { palette, radius, space, type } from "../../../src/theme";
import { Banner, Button, Card, ErrorState, Icon, Loading, Screen } from "../../../src/ui";

/** Best available: choose a category and how many; the server finds neighbouring seats. */
export default function BestAvailableScreen() {
  const { id } = useLocalSearchParams<{ id: string }>();
  const eventId = Number(id);
  const { status } = useSession();
  const signedIn = status === "signedIn";
  const isFocused = useIsFocused();
  const event = useEvent(eventId);
  const categories = useCategories(eventId);
  const best = useBestAvailable();
  const syncHold = useBookingFlow((s) => s.syncHold);

  const [categoryId, setCategoryId] = useState<number | null>(null);
  const [quantity, setQuantity] = useState(2);
  const [cooldownUntil, setCooldownUntil] = useState(0);
  const now = useNow(1000, cooldownUntil > 0);
  const cooldown = Math.max(0, Math.ceil((cooldownUntil - now) / 1000));

  const max = event.data?.max_tickets_per_user ?? 1;
  const available = categories.data?.items.filter((c) => c.seats_available > 0) ?? [];
  const chosen = available.find((c) => c.id === categoryId) ?? null;

  // Start on the cheapest category that has seats.
  const firstId = available[0]?.id ?? null;
  useEffect(() => {
    if (categoryId === null && firstId !== null) setCategoryId(firstId);
  }, [categoryId, firstId]);

  const { mutate } = best;
  const find = useCallback(() => {
    if (categoryId === null) return;
    mutate(
      { event_id: eventId, category_id: categoryId, quantity },
      {
        onSuccess: async (hold) => {
          syncHold(hold.expires_at, hold.remaining_seconds);
          const blockId = hold.seats[0]?.location.block_id;
          await writeStoredCheckout({ holdId: hold.id, eventId, blockId, idempotencyKey: Crypto.randomUUID(), bookingReference: null });
          if (blockId === undefined) return;
          // Show the seats on the map first; checkout is one tap from there.
          router.replace({ pathname: "/event/[id]/block/[blockId]", params: { id, blockId: String(blockId), found: "1" } });
        },
        onError: (error) => {
          if (error instanceof ApiError && error.status === 429) setCooldownUntil(Date.now() + (error.retryAfterSeconds ?? 10) * 1000);
        },
      },
    );
  }, [mutate, categoryId, eventId, quantity, syncHold, id]);

  const continueAfterSignIn = useRef(false);
  useEffect(() => {
    if (signedIn && isFocused && continueAfterSignIn.current) {
      continueAfterSignIn.current = false;
      find();
    }
  }, [signedIn, isFocused, find]);

  if (event.isPending || categories.isPending) return <Loading label="Loading prices…" />;
  if (event.isError) return <ErrorState error={event.error} onRetry={() => void event.refetch()} />;
  if (categories.isError) return <ErrorState error={categories.error} onRetry={() => void categories.refetch()} />;

  const onFind = () => {
    if (!signedIn) {
      continueAfterSignIn.current = true;
      router.push("/sign-in");
      return;
    }
    find();
  };

  return (
    <Screen
      footer={
        <>
          {chosen ? (
            <View style={styles.totalRow}>
              <Text style={type.muted}>
                {quantity} × {chosen.name}
              </Text>
              <Text style={type.price} testID="best-total">{formatKes(chosen.price_cents * quantity)}</Text>
            </View>
          ) : null}
          <Button
            title={cooldown > 0 ? `Try again in ${cooldown}s` : signedIn ? "Find my seats" : "Sign in to find seats"}
            icon="sparkle"
            onPress={onFind}
            disabled={chosen === null || cooldown > 0}
            loading={best.isPending}
            testID="find-seats"
          />
        </>
      }
    >
      <View>
        <Text style={type.h2}>Seats together, picked for you</Text>
        <Text style={type.muted}>Choose a price and how many. We find seats next to each other in one row and hold them while you pay.</Text>
      </View>

      {best.isError ? (
        <Banner
          tone={isEventBusy(best.error) ? "warning" : "error"}
          testID="best-error"
          // The organiser is updating the event, or the network dropped: the same choice can be sent again.
          action={isRetryable(best.error) && cooldown === 0 && !best.isPending ? "Try again" : undefined}
          onAction={find}
        >
          {errorMessage(best.error)}
        </Banner>
      ) : null}

      {available.length === 0 ? (
        <Banner title="Sold out">There are no seats left for this event.</Banner>
      ) : (
        <>
          <Text style={type.h3}>Price</Text>
          <View style={styles.options} accessibilityRole="radiogroup">
            {available.map((c) => {
              const on = c.id === categoryId;
              return (
                <Pressable
                  key={c.id}
                  onPress={() => setCategoryId(c.id)}
                  accessibilityRole="radio"
                  accessibilityState={{ selected: on }}
                  testID={`category-${c.name}`}
                  style={[styles.option, on && styles.optionOn]}
                >
                  <View style={[styles.swatch, { backgroundColor: c.color_hex }]} />
                  <View style={styles.fill}>
                    <Text style={type.h3}>{c.name}</Text>
                    <Text style={type.small}>{formatCount(c.seats_available)} seats left</Text>
                  </View>
                  <Text style={type.label}>{formatKes(c.price_cents)}</Text>
                  <View style={[styles.radio, on && styles.radioOn]}>{on ? <Icon name="check" size={14} color={palette.champagne} /> : null}</View>
                </Pressable>
              );
            })}
          </View>

          <Text style={type.h3}>How many seats</Text>
          <Card style={styles.stepper}>
            <Pressable onPress={() => setQuantity((q) => Math.max(1, q - 1))} disabled={quantity <= 1} accessibilityRole="button" accessibilityLabel="Fewer seats" testID="qty-minus" style={[styles.step, quantity <= 1 && styles.stepOff]}>
              <Icon name="minus" />
            </Pressable>
            <Text style={styles.quantity} testID="qty" accessibilityLabel={`${quantity} seats`}>{quantity}</Text>
            <Pressable onPress={() => setQuantity((q) => Math.min(max, q + 1))} disabled={quantity >= max} accessibilityRole="button" accessibilityLabel="More seats" testID="qty-plus" style={[styles.step, quantity >= max && styles.stepOff]}>
              <Icon name="plus" />
            </Pressable>
          </Card>
          <Text style={type.small}>Up to {max} tickets per person for this event.</Text>
        </>
      )}
    </Screen>
  );
}

const styles = StyleSheet.create({
  fill: { flex: 1 },
  totalRow: { flexDirection: "row", justifyContent: "space-between", alignItems: "center", gap: space.md },
  options: { gap: space.sm },
  option: { flexDirection: "row", alignItems: "center", gap: space.md, padding: space.md, minHeight: 64, borderRadius: radius.md, borderWidth: 1.5, borderColor: palette.grey300, backgroundColor: palette.white },
  optionOn: { borderColor: palette.emeraldInk, backgroundColor: palette.ink50 },
  swatch: { width: 18, height: 18, borderRadius: 5 },
  radio: { width: 24, height: 24, borderRadius: 12, borderWidth: 1.5, borderColor: palette.grey400, alignItems: "center", justifyContent: "center" },
  radioOn: { backgroundColor: palette.emeraldInk, borderColor: palette.emeraldInk },
  stepper: { flexDirection: "row", alignItems: "center", justifyContent: "space-between", paddingVertical: space.sm },
  step: { width: 52, height: 52, borderRadius: 26, borderWidth: 1.5, borderColor: palette.emeraldInk, alignItems: "center", justifyContent: "center" },
  stepOff: { opacity: 0.35 },
  quantity: { ...type.price, fontSize: 32, lineHeight: 40, minWidth: 60, textAlign: "center" },
});
