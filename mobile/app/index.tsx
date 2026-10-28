import { StyleSheet, Text, View } from "react-native";

/** A placeholder home screen until the app shell lands (phone apps story 2). */
export default function Home() {
  return (
    <View style={styles.screen}>
      <Text accessibilityRole="header" style={styles.title}>
        Just a Bill
      </Text>
      <Text style={styles.body}>Bills, your votes and your representatives are coming to this app.</Text>
    </View>
  );
}

const styles = StyleSheet.create({
  screen: { flex: 1, alignItems: "center", justifyContent: "center", padding: 24, gap: 12 },
  title: { fontSize: 28, fontWeight: "700" },
  body: { fontSize: 17, textAlign: "center" },
});
